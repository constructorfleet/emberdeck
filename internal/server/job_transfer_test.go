package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
	"github.com/ConstructorFleet/emberdeck/jobtransfer"
)

func TestChunkedWorkerFetchesImmutableProgramUnderItsLease(t *testing.T) {
	s := openServer(t)
	createdCredential := request(t, s, http.MethodPost, "/api/v1/credentials", `{"name":"chunk worker","role":"worker","driver_ids":["bench"]}`, "test-key")
	var worker struct {
		Key string `json:"key"`
	}
	if createdCredential.Code != http.StatusCreated {
		t.Fatalf("create worker credential: %d %s", createdCredential.Code, createdCredential.Body.String())
	}
	if err := json.Unmarshal(createdCredential.Body.Bytes(), &worker); err != nil || worker.Key == "" {
		t.Fatalf("worker credential response: %s (%v)", createdCredential.Body.String(), err)
	}
	const source = "G21\nG90\nM5\n"
	upload := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"transfer.gcode","gcode":"G21\nG90\nM5\n"}`, "test-key")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	workerPoll := `{"session":"device-session","capabilities":["` + jobtransfer.Capability + `"]}`
	request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", workerPoll, worker.Key)
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+created.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if started.Code != http.StatusOK {
		t.Fatalf("start job: %d %s", started.Code, started.Body.String())
	}
	pickup := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", workerPoll, worker.Key)
	var assignment struct {
		Command  string `json:"command"`
		Protocol int    `json:"protocol"`
		Lease    string `json:"lease"`
		Job      struct {
			ID      string               `json:"id"`
			GCode   string               `json:"gcode"`
			Content jobtransfer.Manifest `json:"content"`
		} `json:"job"`
	}
	if err := json.Unmarshal(pickup.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.Command != "run" || assignment.Protocol != 2 || assignment.Lease == "" || assignment.Job.ID != created.ID || assignment.Job.GCode != "" {
		t.Fatalf("unexpected chunked assignment: %s", pickup.Body.String())
	}
	if assignment.Job.Content.Size != int64(len(source)) || assignment.Job.Content.SHA256 == "" {
		t.Fatalf("assignment content manifest = %+v", assignment.Job.Content)
	}

	fetch := func(session, lease, byteRange string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/drivers/bench/jobs/"+created.ID+"/content", nil)
		r.Header.Set("Authorization", "Bearer "+worker.Key)
		r.Header.Set("X-EmberDeck-Session", session)
		r.Header.Set("X-EmberDeck-Lease", lease)
		r.Header.Set("Range", byteRange)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	first := fetch("device-session", assignment.Lease, "bytes=0-4")
	if first.Code != http.StatusPartialContent || first.Header().Get("Content-Range") != fmt.Sprintf("bytes 0-4/%d", len(source)) || first.Header().Get("X-EmberDeck-Content-SHA256") != assignment.Job.Content.SHA256 || first.Body.String() != source[:5] {
		t.Fatalf("first chunk: %d headers=%v body=%q", first.Code, first.Header(), first.Body.String())
	}
	last := fetch("device-session", assignment.Lease, fmt.Sprintf("bytes=5-%d", len(source)-1))
	if last.Code != http.StatusPartialContent || last.Body.String() != source[5:] {
		t.Fatalf("last chunk: %d body=%q", last.Code, last.Body.String())
	}
	for name, got := range map[string]*httptest.ResponseRecorder{
		"wrong session": fetch("other-session", assignment.Lease, "bytes=0-1"),
		"wrong lease":   fetch("device-session", "other-lease", "bytes=0-1"),
	} {
		t.Run(name, func(t *testing.T) {
			if got.Code != http.StatusConflict {
				t.Fatalf("content request status = %d, want %d: %s", got.Code, http.StatusConflict, got.Body.String())
			}
		})
	}
	if got := fetch("device-session", assignment.Lease, fmt.Sprintf("bytes=%d-%d", len(source), len(source))); got.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("out-of-range request status = %d, want 416: %s", got.Code, got.Body.String())
	}
	wrongDriver := httptest.NewRequest(http.MethodGet, "/api/v1/drivers/other/jobs/"+created.ID+"/content", nil)
	wrongDriver.Header.Set("Authorization", "Bearer "+worker.Key)
	wrongDriver.Header.Set("X-EmberDeck-Session", "device-session")
	wrongDriver.Header.Set("X-EmberDeck-Lease", assignment.Lease)
	wrongDriver.Header.Set("Range", "bytes=0-1")
	wrongDriverResponse := httptest.NewRecorder()
	s.ServeHTTP(wrongDriverResponse, wrongDriver)
	if wrongDriverResponse.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope worker content status = %d, want 403", wrongDriverResponse.Code)
	}
	terminal := fmt.Sprintf(`{"session":"device-session","job_id":%q,"lease":%q,"state":"failed","acked":0,"total":3}`, created.ID, assignment.Lease)
	if got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", terminal, worker.Key); got.Code != http.StatusOK {
		t.Fatalf("terminal receipt: %d %s", got.Code, got.Body.String())
	}
	if got := fetch("device-session", assignment.Lease, "bytes=0-1"); got.Code != http.StatusConflict {
		t.Fatalf("terminal lease content status = %d, want 409", got.Code)
	}
}

func TestProtocolTwoAssignmentOmitsLargeDesignerProvenanceButLegacyAssignmentKeepsFullJob(t *testing.T) {
	s := openServer(t)
	profileID := createTestProfile(t, s)
	objects := make([]server.VectorObject, 40)
	for i := range objects {
		x := float64(i + 10)
		objects[i] = server.VectorObject{
			Kind: "path", ID: fmt.Sprintf("path-%d", i), OperationID: "engrave",
			Subpaths: []server.VectorSubpath{{Points: []server.VectorPoint{
				{XMM: x, YMM: 10}, {XMM: x + 1, YMM: 11},
			}}},
		}
	}
	projectBody, err := json.Marshal(struct {
		SchemaVersion   int                      `json:"schema_version"`
		Name            string                   `json:"name"`
		Units           string                   `json:"units"`
		ProfileID       string                   `json:"profile_id"`
		ProfileRevision int                      `json:"profile_revision"`
		Objects         []server.VectorObject    `json:"objects"`
		Operations      []server.VectorOperation `json:"operations"`
	}{2, "many paths", "mm", profileID, 1, objects,
		[]server.VectorOperation{{ID: "engrave", Type: "vector-engrave", FeedMMMin: 600, PowerPercent: 25}}})
	if err != nil {
		t.Fatal(err)
	}
	project := request(t, s, http.MethodPost, "/api/v1/projects", string(projectBody), "test-key")
	var saved struct {
		ID string `json:"id"`
	}
	if project.Code != http.StatusCreated || json.Unmarshal(project.Body.Bytes(), &saved) != nil {
		t.Fatalf("create large designer project: %d %s", project.Code, project.Body.String())
	}
	generated := request(t, s, http.MethodPost, "/api/v1/projects/"+saved.ID+"/generate", `{"revision":1}`, "test-key")
	var job struct {
		ID string `json:"id"`
	}
	if generated.Code != http.StatusCreated || json.Unmarshal(generated.Body.Bytes(), &job) != nil {
		t.Fatalf("generate large-provenance job: %d %s", generated.Code, generated.Body.String())
	}
	request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll",
		`{"session":"device-session","capabilities":["`+jobtransfer.Capability+`"],"controller_settings":{"$20":0,"$30":1000,"$31":0,"$32":1,"$130":300,"$131":200}}`, "test-key")
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true}`, "test-key")
	if started.Code != http.StatusOK {
		t.Fatalf("start designer job: %d %s", started.Code, started.Body.String())
	}
	chunked := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll",
		`{"session":"device-session","capabilities":["`+jobtransfer.Capability+`"],"controller_settings":{"$20":0,"$30":1000,"$31":0,"$32":1,"$130":300,"$131":200}}`, "test-key")
	var chunkedBody map[string]json.RawMessage
	if err := json.Unmarshal(chunked.Body.Bytes(), &chunkedBody); err != nil {
		t.Fatal(err)
	}
	var chunkedJob map[string]json.RawMessage
	if err := json.Unmarshal(chunkedBody["job"], &chunkedJob); err != nil {
		t.Fatal(err)
	}
	if chunked.Code != http.StatusOK || len(chunked.Body.Bytes()) > 4096 {
		t.Fatalf("protocol-2 assignment size = %d, status=%d; device limit is 4096: %s", len(chunked.Body.Bytes()), chunked.Code, chunked.Body.String())
	}
	for _, field := range []string{"id", "total", "size", "content", "expected_settings"} {
		if len(chunkedJob[field]) == 0 {
			t.Errorf("protocol-2 assignment omitted Go worker field %q: %s", field, chunked.Body.String())
		}
	}
	for _, field := range []string{"provenance", "name", "created", "execution", "review", "gcode"} {
		if len(chunkedJob[field]) != 0 {
			t.Errorf("protocol-2 assignment included non-worker field %q", field)
		}
	}

	legacyUpload := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"legacy.gcode","gcode":"G21\nG90\nM5\n"}`, "test-key")
	var legacyCreated struct {
		ID string `json:"id"`
	}
	if legacyUpload.Code != http.StatusCreated || json.Unmarshal(legacyUpload.Body.Bytes(), &legacyCreated) != nil {
		t.Fatalf("create legacy job: %d %s", legacyUpload.Code, legacyUpload.Body.String())
	}
	request(t, s, http.MethodPost, "/api/v1/drivers/legacy/poll", `{"session":"legacy-session"}`, "test-key")
	legacyStart := request(t, s, http.MethodPost, "/api/v1/jobs/"+legacyCreated.ID+"/start", `{"driver_id":"legacy","confirmed":true,"bounds_override":true}`, "test-key")
	if legacyStart.Code != http.StatusOK {
		t.Fatalf("start legacy job: %d %s", legacyStart.Code, legacyStart.Body.String())
	}
	legacyPickup := request(t, s, http.MethodPost, "/api/v1/drivers/legacy/poll", `{"session":"legacy-session"}`, "test-key")
	var legacyBody map[string]json.RawMessage
	if err := json.Unmarshal(legacyPickup.Body.Bytes(), &legacyBody); err != nil {
		t.Fatal(err)
	}
	var legacyJob map[string]json.RawMessage
	if err := json.Unmarshal(legacyBody["job"], &legacyJob); err != nil {
		t.Fatal(err)
	}
	if string(legacyBody["protocol"]) != "1" || len(legacyJob["name"]) == 0 || len(legacyJob["created"]) == 0 || len(legacyJob["gcode"]) == 0 {
		t.Fatalf("legacy protocol-1 assignment changed: %s", legacyPickup.Body.String())
	}
}

func TestServerRejectsProgramsAboveTheWorkerTransferLimit(t *testing.T) {
	s := openServer(t)
	program := "M5" + strings.Repeat(" ", jobtransfer.MaxProgramBytes)
	body := `{"name":"oversized.gcode","gcode":"` + program + `"}`
	response := request(t, s, http.MethodPost, "/api/v1/jobs", body, "test-key")
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "4 MiB maximum") {
		t.Fatalf("oversized program status = %d %s", response.Code, response.Body.String())
	}
}

func TestUploadAllowsNearLimitProgramAfterJSONEscaping(t *testing.T) {
	s := openServer(t)
	program := strings.Repeat("(x)\n", (jobtransfer.MaxProgramBytes-3)/4) + "M5\n"
	body, err := json.Marshal(map[string]string{"name": "near-limit.gcode", "gcode": program})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, s, http.MethodPost, "/api/v1/jobs", string(body), "test-key")
	if response.Code != http.StatusCreated {
		t.Fatalf("near-limit program was rejected: %d %s", response.Code, response.Body.String())
	}
	var created struct {
		Size int64 `json:"size"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Size != int64(len(program)) {
		t.Fatalf("stored size = %d, want %d", created.Size, len(program))
	}
}

func TestLegacyAssignmentRespectsOldWorkerResponseLimit(t *testing.T) {
	s := openServer(t)
	program := strings.Repeat("(<<)\n", 700000) + "M5\n"
	// Use literal '<' in the request, as normal browser JSON serialization does,
	// while escaping the line feed required by the JSON string.
	body := `{"name":"legacy-large.gcode","gcode":"` + strings.ReplaceAll(program, "\n", `\n`) + `"}`
	upload := request(t, s, http.MethodPost, "/api/v1/jobs", body, "test-key")
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload legacy compatibility fixture: %d %s", upload.Code, upload.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"legacy-session"}`, "test-key")
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+created.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if started.Code != http.StatusRequestEntityTooLarge || !strings.Contains(started.Body.String(), "legacy worker") {
		t.Fatalf("oversized v1 job start = %d %s", started.Code, started.Body.String())
	}
	stored := request(t, s, http.MethodGet, "/api/v1/jobs/"+created.ID, "", "test-key")
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(stored.Body.Bytes(), &state); err != nil || state.State != "queued" {
		t.Fatalf("legacy worker size rejection changed queued job state: %s (%v)", stored.Body.String(), err)
	}
}

func TestChunkedWorkerCannotDecreaseAcknowledgedProgress(t *testing.T) {
	s := openChunkedWorkerServer(t)
	jobID, lease, workerKey := startChunkedWorkerJob(t, s, "G21\nG90\nM5\n")

	progress := func(acked int) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"session":"device-session","job_id":%q,"lease":%q,"state":"running","controller_state":"Run","acked":%d,"total":3}`, jobID, lease, acked)
		return request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", body, workerKey)
	}
	if got := progress(2); got.Code != http.StatusOK {
		t.Fatalf("initial progress: %d %s", got.Code, got.Body.String())
	}
	if got := progress(1); got.Code != http.StatusBadRequest {
		t.Fatalf("decreasing progress: %d %s, want 400", got.Code, got.Body.String())
	}
	page := listChunkedJobs(t, s, "?state=running")
	if page.Total != 1 || page.Jobs[0]["acked"] != float64(2) || page.Jobs[0]["total"] != float64(3) {
		t.Fatalf("rejected decreasing receipt changed latest progress: %+v", page.Jobs)
	}
}

func TestChunkedWorkerTerminalReceiptRetryIsIdempotentAndConflictsWhenChanged(t *testing.T) {
	s := openChunkedWorkerServer(t)
	jobID, lease, workerKey := startChunkedWorkerJob(t, s, "G21\nG90\nM5\n")
	terminal := func(state string, acked, total int, controllerState, errorText, execution string) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"session":"device-session","job_id":%q,"lease":%q,"state":%q,"acked":%d,"total":%d,"controller_state":%q,"error":%q,"execution":%s}`, jobID, lease, state, acked, total, controllerState, errorText, execution)
		return request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", body, workerKey)
	}
	const (
		state           = "failed"
		acked           = 1
		total           = 3
		controllerState = "Alarm:1"
		errorText       = "controller alarm"
		execution       = `{"path_progress":{"completed":0,"total":0,"confidence":"unknown"}}`
	)

	// Treat the first response as lost; retrying the identical receipt must succeed.
	if got := terminal(state, acked, total, controllerState, errorText, execution); got.Code != http.StatusOK {
		t.Fatalf("first terminal receipt: %d %s", got.Code, got.Body.String())
	}
	if got := terminal(state, acked, total, controllerState, errorText, execution); got.Code != http.StatusOK {
		t.Fatalf("identical terminal receipt retry: %d %s", got.Code, got.Body.String())
	}
	changed := []struct {
		name, state, controllerState, errorText, execution string
		acked, total                                       int
	}{
		{"state", "completed", controllerState, errorText, execution, acked, total},
		{"acknowledgement", state, controllerState, errorText, execution, 2, total},
		{"total", state, controllerState, errorText, execution, acked, 4},
		{"controller state", state, "Alarm:2", errorText, execution, acked, total},
		{"error", state, controllerState, "different alarm", execution, acked, total},
		{"execution omitted", state, controllerState, errorText, "null", acked, total},
		{"execution telemetry", state, controllerState, errorText, `{"path_progress":{"completed":0,"total":0,"confidence":"ambiguous"}}`, acked, total},
	}
	for _, test := range changed {
		t.Run(test.name, func(t *testing.T) {
			if got := terminal(test.state, test.acked, test.total, test.controllerState, test.errorText, test.execution); got.Code != http.StatusConflict {
				t.Fatalf("changed terminal receipt: %d %s, want 409", got.Code, got.Body.String())
			}
		})
	}
	page := listChunkedJobs(t, s, "?state=failed")
	if page.Total != 1 || page.Jobs[0]["acked"] != float64(acked) || page.Jobs[0]["total"] != float64(total) || page.Jobs[0]["error"] != errorText || page.Jobs[0]["controller_state"] != controllerState {
		t.Fatalf("terminal receipt retry changed the committed result: %+v", page.Jobs)
	}
}

func TestChunkedWorkerTerminalReceiptRetrySurvivesJobDeletionAndRestart(t *testing.T) {
	dir := t.TempDir()
	s := openChunkedWorkerServerAt(t, dir)
	jobID, lease, workerKey := startChunkedWorkerJob(t, s, "G21\nG90\nM5\n")
	receipt := fmt.Sprintf(`{"session":"device-session","job_id":%q,"lease":%q,"state":"failed","acked":1,"total":3,"controller_state":"Alarm:1","error":"controller alarm","execution":{"path_progress":{"completed":0,"total":0,"confidence":"unknown"}}}`, jobID, lease)
	if got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", receipt, workerKey); got.Code != http.StatusOK {
		t.Fatalf("first terminal receipt: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, s, http.MethodDelete, "/api/v1/jobs/"+jobID, "", bootstrapKey); got.Code != http.StatusNoContent {
		t.Fatalf("delete terminal job: %d %s", got.Code, got.Body.String())
	}
	secondUpload := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"newer-receipt.gcode","gcode":"G21\nG90\nM5\n"}`, bootstrapKey)
	if secondUpload.Code != http.StatusCreated {
		t.Fatalf("upload newer job: %d %s", secondUpload.Code, secondUpload.Body.String())
	}
	var secondCreated struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(secondUpload.Body.Bytes(), &secondCreated); err != nil || secondCreated.ID == "" {
		t.Fatalf("newer job response: %s (%v)", secondUpload.Body.String(), err)
	}
	secondJobID := secondCreated.ID
	startSecond := request(t, s, http.MethodPost, "/api/v1/jobs/"+secondJobID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, bootstrapKey)
	if startSecond.Code != http.StatusOK {
		t.Fatalf("start newer job: %d %s", startSecond.Code, startSecond.Body.String())
	}
	workerPoll := `{"session":"device-session","capabilities":["` + jobtransfer.Capability + `"]}`
	secondPickup := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", workerPoll, workerKey)
	if secondPickup.Code != http.StatusOK {
		t.Fatalf("pick up newer job: %d %s", secondPickup.Code, secondPickup.Body.String())
	}
	var secondAssignment struct {
		Lease string `json:"lease"`
	}
	if err := json.Unmarshal(secondPickup.Body.Bytes(), &secondAssignment); err != nil || secondAssignment.Lease == "" {
		t.Fatalf("newer assignment: %s (%v)", secondPickup.Body.String(), err)
	}
	secondLease := secondAssignment.Lease
	secondReceipt := fmt.Sprintf(`{"session":"device-session","job_id":%q,"lease":%q,"state":"failed","acked":1,"total":3,"controller_state":"Alarm:1","error":"newer controller alarm","execution":{"path_progress":{"completed":0,"total":0,"confidence":"unknown"}}}`, secondJobID, secondLease)
	if got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", secondReceipt, workerKey); got.Code != http.StatusOK {
		t.Fatalf("newer terminal receipt: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, s, http.MethodDelete, "/api/v1/jobs/"+secondJobID, "", bootstrapKey); got.Code != http.StatusNoContent {
		t.Fatalf("delete newer terminal job: %d %s", got.Code, got.Body.String())
	}
	indexData, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		TerminalReceipts map[string]json.RawMessage `json:"terminal_receipts"`
	}
	if err := json.Unmarshal(indexData, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.TerminalReceipts) != 1 {
		t.Fatalf("pending tombstones = %d, want one for the driver", len(index.TerminalReceipts))
	}
	if _, ok := index.TerminalReceipts[secondJobID]; !ok {
		t.Fatalf("latest tombstone missing for job %s: %v", secondJobID, index.TerminalReceipts)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := openChunkedWorkerServerAt(t, dir)
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", workerPoll, workerKey); got.Code != http.StatusOK {
		t.Fatalf("register worker after restart: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", secondReceipt, workerKey); got.Code != http.StatusOK {
		t.Fatalf("latest receipt after deletion and restart: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", receipt, workerKey); got.Code != http.StatusConflict {
		t.Fatalf("older receipt after newer terminal receipt: %d %s, want 409", got.Code, got.Body.String())
	}
	changedReceipt := strings.Replace(receipt, `"acked":1`, `"acked":2`, 1)
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", changedReceipt, workerKey); got.Code != http.StatusConflict {
		t.Fatalf("changed receipt after deletion: %d %s, want 409", got.Code, got.Body.String())
	}
	wrongDriver := request(t, restarted, http.MethodPost, "/api/v1/drivers/other/poll", receipt, workerKey)
	if wrongDriver.Code != http.StatusForbidden {
		t.Fatalf("receipt on unauthorized driver: %d %s, want 403", wrongDriver.Code, wrongDriver.Body.String())
	}
	wrongLease := strings.Replace(receipt, lease, strings.Repeat("0", len(lease)), 1)
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", wrongLease, workerKey); got.Code != http.StatusConflict {
		t.Fatalf("receipt with wrong lease after deletion: %d %s, want 409", got.Code, got.Body.String())
	}
}

func TestLegacyTerminalReceiptRetryRejectsRemovedExecutionTelemetry(t *testing.T) {
	dir := t.TempDir()
	s := openChunkedWorkerServerAt(t, dir)
	jobID, lease, workerKey := startChunkedWorkerJob(t, s, "G21\nG90\nM5\n")
	const original = `{"session":"device-session","job_id":%q,"lease":%q,"state":"failed","acked":1,"total":3,"controller_state":"Alarm:1","error":"controller alarm","execution":{"path_progress":{"completed":0,"total":0,"confidence":"unknown"}}}`
	receipt := fmt.Sprintf(original, jobID, lease)
	if got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", receipt, workerKey); got.Code != http.StatusOK {
		t.Fatalf("first terminal receipt: %d %s", got.Code, got.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	removeTerminalReceiptDigest(t, dir, jobID)

	restarted := openChunkedWorkerServerAt(t, dir)
	workerPoll := `{"session":"device-session","capabilities":["` + jobtransfer.Capability + `"]}`
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", workerPoll, workerKey); got.Code != http.StatusOK {
		t.Fatalf("register worker after restart: %d %s", got.Code, got.Body.String())
	}
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", receipt, workerKey); got.Code != http.StatusOK {
		t.Fatalf("identical legacy terminal receipt retry: %d %s", got.Code, got.Body.String())
	}
	withoutExecution := fmt.Sprintf(`{"session":"device-session","job_id":%q,"lease":%q,"state":"failed","acked":1,"total":3,"controller_state":"Alarm:1","error":"controller alarm"}`, jobID, lease)
	if got := request(t, restarted, http.MethodPost, "/api/v1/drivers/bench/poll", withoutExecution, workerKey); got.Code != http.StatusConflict {
		t.Fatalf("legacy terminal receipt without stored execution telemetry: %d %s, want 409", got.Code, got.Body.String())
	}
}

func startChunkedWorkerJob(t *testing.T, s http.Handler, source string) (jobID, lease, workerKey string) {
	t.Helper()
	worker := createCredential(t, s, "chunk transfer worker", "worker", []string{"bench"})
	payload, err := json.Marshal(map[string]string{"name": "receipt-contract.gcode", "gcode": source})
	if err != nil {
		t.Fatal(err)
	}
	upload := request(t, s, http.MethodPost, "/api/v1/jobs", string(payload), bootstrapKey)
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload job: %d %s", upload.Code, upload.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(upload.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("uploaded job: %s (%v)", upload.Body.String(), err)
	}
	jobID = created.ID
	workerPoll := `{"session":"device-session","capabilities":["` + jobtransfer.Capability + `"]}`
	if got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", workerPoll, worker.Key); got.Code != http.StatusOK {
		t.Fatalf("register chunked worker: %d %s", got.Code, got.Body.String())
	}
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+jobID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, bootstrapKey)
	if started.Code != http.StatusOK {
		t.Fatalf("start job: %d %s", started.Code, started.Body.String())
	}
	pickup := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", workerPoll, worker.Key)
	if pickup.Code != http.StatusOK {
		t.Fatalf("pick up job: %d %s", pickup.Code, pickup.Body.String())
	}
	var assignment struct {
		Lease string `json:"lease"`
		Job   struct {
			ID string `json:"id"`
		} `json:"job"`
	}
	if err := json.Unmarshal(pickup.Body.Bytes(), &assignment); err != nil || assignment.Job.ID != jobID || assignment.Lease == "" {
		t.Fatalf("chunked assignment: %s (%v)", pickup.Body.String(), err)
	}
	return jobID, assignment.Lease, worker.Key
}

func openChunkedWorkerServer(t *testing.T) *server.Server {
	return openChunkedWorkerServerAt(t, t.TempDir())
}

func openChunkedWorkerServerAt(t *testing.T, dir string) *server.Server {
	t.Helper()
	s, err := server.OpenWithPlugins(dir, bootstrapKey, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func listChunkedJobs(t *testing.T, h http.Handler, query string) jobPage {
	t.Helper()
	response := request(t, h, http.MethodGet, "/api/v1/jobs"+query, "", bootstrapKey)
	if response.Code != http.StatusOK {
		t.Fatalf("list jobs: %d %s", response.Code, response.Body.String())
	}
	var page jobPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func removeTerminalReceiptDigest(t *testing.T, dir, jobID string) {
	t.Helper()
	path := filepath.Join(dir, "jobs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var index struct {
		Version  int                        `json:"version"`
		EventSeq int64                      `json:"event_seq"`
		Jobs     map[string]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(index.Jobs[jobID], &record); err != nil {
		t.Fatal(err)
	}
	delete(record, "terminal_receipt_digest")
	index.Jobs[jobID], err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
