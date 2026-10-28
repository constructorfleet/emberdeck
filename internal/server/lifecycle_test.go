package server_test

import (
	"encoding/json"
	"github.com/ConstructorFleet/emberdeck/internal/server"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOperatorDispatchesToRegisteredDriverAndMonitorsCompletion(t *testing.T) {
	s := openServer(t)
	uploaded := request(t, s, "POST", "/api/v1/jobs", `{"name":"test","gcode":"M5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(uploaded.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
	var assignment struct {
		Job     struct{ ID string }
		Lease   string
		Command string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.Job.ID != job.ID || assignment.Lease == "" || assignment.Command != "run" {
		t.Fatalf("assignment: %s", w.Body.String())
	}
	body := `{"session":"session-a","job_id":"` + job.ID + `","lease":"` + assignment.Lease + `","state":"completed","acked":1,"total":1}`
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", body, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	var result struct {
		State string
		Acked int
		Total int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || result.Acked != 1 || result.Total != 1 {
		t.Fatalf("progress lost: %s", w.Body.String())
	}
}

func openServer(t *testing.T) *server.Server {
	t.Helper()
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDriverCannotClaimAnotherSessionOrForgeProgress(t *testing.T) {
	s := openServer(t)
	w := request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"owner"}`, "test-key")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"other"}`, "test-key")
	if w.Code != 409 {
		t.Fatalf("duplicate driver ownership: %d", w.Code)
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"owner","job_id":"made-up","lease":"forged","state":"completed","acked":1,"total":1}`, "test-key")
	if w.Code != 409 {
		t.Fatalf("forged progress: %d", w.Code)
	}
}

func TestWorkerPersistsControllerPositionSeparatelyFromAcknowledgedLines(t *testing.T) {
	s := openServer(t)
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"path.gcode","gcode":"G21 G90\nG0 X0 Y0\nM4 S100\nG1 X10 Y0 F300"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
	var assignment struct {
		Lease string `json:"lease"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	body := `{"session":"session-a","job_id":"` + job.ID + `","lease":"` + assignment.Lease + `","state":"running","acked":4,"total":4,"execution":{"work_position":{"x":5,"y":0},"position_units":"assumed-mm","position_reported_at":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","path_progress":{"completed":0,"total":1,"active":0,"confidence":"estimated"}}}`
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", body, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("progress: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	var stored struct {
		Acked     int `json:"acked"`
		Execution struct {
			WorkPosition struct {
				X float64 `json:"x"`
			} `json:"work_position"`
			Progress struct {
				Completed  int    `json:"completed"`
				Active     *int   `json:"active"`
				Confidence string `json:"confidence"`
			} `json:"path_progress"`
		} `json:"execution"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Acked != 4 || stored.Execution.WorkPosition.X != 5 || stored.Execution.Progress.Completed != 0 || stored.Execution.Progress.Active == nil || *stored.Execution.Progress.Active != 0 || stored.Execution.Progress.Confidence != "estimated" {
		t.Fatalf("acknowledged lines were confused with path execution: %s", w.Body.String())
	}
}

func TestRestartInterruptsWorkWithoutReassigningIt(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"test","gcode":"M5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"owner"}`, "test-key")
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench"}`, "test-key")
	if w.Code != 400 {
		t.Fatalf("missing confirmation accepted: %d", w.Code)
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("start: %s", w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	w = request(t, restarted, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	var result struct{ State string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "interrupted" {
		t.Fatalf("restart retained active state: %s", w.Body.String())
	}
	w = request(t, restarted, "POST", "/api/v1/drivers/bench/poll", `{"session":"new-owner"}`, "test-key")
	if strings.Contains(w.Body.String(), `"command":"run"`) {
		t.Fatal("restart reassigned interrupted work")
	}
}

func TestInvalidMachineProgramIsRejectedAtUploadWithLineNumber(t *testing.T) {
	s := openServer(t)
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"test","gcode":"G1 X1!"}`, "test-key")
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "line 1") {
		t.Fatalf("unsafe program upload = %d %s", w.Code, w.Body.String())
	}
}

func TestPhysicalCompletionWinsRaceWithCancellation(t *testing.T) {
	s := openServer(t)
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"test","gcode":"M5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"owner"}`, "test-key")
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	var assignment struct{ Lease string }
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/cancel", `{}`, "test-key")
	body := `{"session":"owner","job_id":"` + job.ID + `","lease":"` + assignment.Lease + `","state":"completed","acked":1,"total":1}`
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", body, "test-key")
	if w.Code != 200 {
		t.Fatalf("Idle completion lost cancellation race: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	var result struct{ State string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("job remains active: %s", w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/drivers", "", "test-key")
	if strings.Contains(w.Body.String(), `"busy":true`) {
		t.Fatal("finished worker remains busy")
	}
}

func TestRestartInterruptsPendingCancellation(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"test","gcode":"M5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"owner"}`, "test-key")
	request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/cancel", `{}`, "test-key")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	w = request(t, restarted, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	var result struct{ State string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "interrupted" {
		t.Fatalf("restart left cancellation pending forever: %s", w.Body.String())
	}
}
