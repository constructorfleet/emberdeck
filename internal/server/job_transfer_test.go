package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
