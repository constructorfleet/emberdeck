package server_test

import (
	"encoding/json"
	"github.com/ConstructorFleet/emberdeck/internal/server"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPauseAndResumeFollowConfirmedControllerState(t *testing.T) {
	s := openServer(t)
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"cut","gcode":"G1 X1\nM5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["pause_resume"]}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("register capable worker: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	var assignment struct{ Lease string }
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	poll := func(state, controller string) *httptest.ResponseRecorder {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"session": "owner", "capabilities": []string{"pause_resume"}, "job_id": job.ID, "lease": assignment.Lease, "state": state, "controller_state": controller, "acked": 1, "total": 2})
		return request(t, s, "POST", "/api/v1/drivers/bench/poll", string(payload), "test-key")
	}
	state := func(want string) {
		t.Helper()
		w := request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
		var result struct{ State string }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.State != want {
			t.Fatalf("state=%s, want %s: %s", result.State, want, w.Body.String())
		}
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/pause", `{}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	state("pausing")
	w = poll("pausing", "Hold:1")
	if w.Code != 200 {
		t.Fatalf("decelerating: %d %s", w.Code, w.Body.String())
	}
	state("pausing")
	w = poll("paused", "Hold:0")
	if w.Code != 200 {
		t.Fatalf("confirmed hold: %d %s", w.Code, w.Body.String())
	}
	state("paused")
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/resume", `{}`, "test-key")
	if w.Code != 400 {
		t.Fatalf("unconfirmed resume accepted: %d", w.Code)
	}
	state("paused")
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/resume", `{"confirmed":true}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	state("resuming")
	w = poll("paused", "Hold:0")
	if w.Code != 200 {
		t.Fatalf("pending resume: %d %s", w.Code, w.Body.String())
	}
	state("resuming")
	w = poll("running", "Run")
	if w.Code != 200 {
		t.Fatalf("confirmed resume: %d %s", w.Code, w.Body.String())
	}
	state("running")
}

func assignControlJob(t *testing.T, s http.Handler, capable bool) (string, string) {
	t.Helper()
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"cut","gcode":"G1 X1\nM5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	body := `{"session":"owner"}`
	if capable {
		body = `{"session":"owner","capabilities":["pause_resume"]}`
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", body, "test-key")
	if w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var assignment struct{ Lease string }
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	return job.ID, assignment.Lease
}
func reportControl(t *testing.T, s http.Handler, id, lease, state, controller string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"session": "owner", "capabilities": []string{"pause_resume"}, "job_id": id, "lease": lease, "state": state, "controller_state": controller, "acked": 1, "total": 2})
	return request(t, s, "POST", "/api/v1/drivers/bench/poll", string(payload), "test-key")
}

func TestPauseRequiresCapableWorkerAndStableControllerHold(t *testing.T) {
	t.Run("older worker", func(t *testing.T) {
		s := openServer(t)
		id, _ := assignControlJob(t, s, false)
		w := request(t, s, "POST", "/api/v1/jobs/"+id+"/pause", `{}`, "test-key")
		if w.Code != 409 {
			t.Fatalf("unsupported pause returned %d", w.Code)
		}
	})
	t.Run("decelerating is not paused", func(t *testing.T) {
		s := openServer(t)
		id, lease := assignControlJob(t, s, true)
		request(t, s, "POST", "/api/v1/jobs/"+id+"/pause", `{}`, "test-key")
		w := reportControl(t, s, id, lease, "paused", "Hold:1")
		if w.Code != 400 {
			t.Fatalf("decelerating accepted as held: %d %s", w.Code, w.Body.String())
		}
		w = request(t, s, "POST", "/api/v1/jobs/"+id+"/resume", `{"confirmed":true}`, "test-key")
		if w.Code != 409 {
			t.Fatalf("resumed before hold confirmed: %d", w.Code)
		}
	})
}

func TestHeldJobRetainsExclusiveDriverOwnershipAndCanBeCanceled(t *testing.T) {
	s := openServer(t)
	id, lease := assignControlJob(t, s, true)
	request(t, s, "POST", "/api/v1/jobs/"+id+"/pause", `{}`, "test-key")
	if w := reportControl(t, s, id, lease, "paused", "Hold:0"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w := request(t, s, "DELETE", "/api/v1/jobs/"+id, "", "test-key")
	if w.Code != 409 {
		t.Fatalf("deleted active held job: %d", w.Code)
	}
	w = request(t, s, "GET", "/api/v1/drivers", "", "test-key")
	var drivers []struct{ Busy bool }
	if err := json.Unmarshal(w.Body.Bytes(), &drivers); err != nil {
		t.Fatal(err)
	}
	if len(drivers) != 1 || !drivers[0].Busy {
		t.Fatalf("held driver released: %s", w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", `{}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("held cancel: %d %s", w.Code, w.Body.String())
	}
	w = reportControl(t, s, id, lease, "canceled", "Hold:0")
	if w.Code != 200 {
		t.Fatalf("stopped cancellation: %d %s", w.Code, w.Body.String())
	}
}

func TestRestartInterruptsEveryPauseState(t *testing.T) {
	for _, state := range []string{"pausing", "paused", "resuming"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			s, err := server.Open(dir, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			id, lease := assignControlJob(t, s, true)
			request(t, s, "POST", "/api/v1/jobs/"+id+"/pause", `{}`, "test-key")
			if state != "pausing" {
				if w := reportControl(t, s, id, lease, "paused", "Hold:0"); w.Code != 200 {
					t.Fatal(w.Body.String())
				}
			}
			if state == "resuming" {
				request(t, s, "POST", "/api/v1/jobs/"+id+"/resume", `{"confirmed":true}`, "test-key")
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := server.Open(dir, "test-key")
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			w := request(t, reopened, "GET", "/api/v1/jobs/"+id, "", "test-key")
			var job struct {
				State string
				Lease string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
			if job.State != "interrupted" || job.Lease != "" {
				t.Fatalf("restart preserves execution authority: %s", w.Body.String())
			}
		})
	}
}

func TestCapableWorkerCannotCompleteWithoutObservedIdle(t *testing.T) {
	s := openServer(t)
	id, lease := assignControlJob(t, s, true)
	payload, _ := json.Marshal(map[string]any{"session": "owner", "capabilities": []string{"pause_resume"}, "job_id": id, "lease": lease, "state": "completed", "controller_state": "Run", "acked": 2, "total": 2})
	w := request(t, s, "POST", "/api/v1/drivers/bench/poll", string(payload), "test-key")
	if w.Code != 409 {
		t.Fatalf("moving controller marked complete: %d %s", w.Code, w.Body.String())
	}
}

func TestPausedWorkerExpiryInterruptsAndRevokesLease(t *testing.T) {
	t.Parallel()
	s := openServer(t)
	id, lease := assignControlJob(t, s, true)
	request(t, s, "POST", "/api/v1/jobs/"+id+"/pause", `{}`, "test-key")
	if w := reportControl(t, s, id, lease, "paused", "Hold:0"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(12 * time.Second)
	for {
		w := request(t, s, "GET", "/api/v1/jobs/"+id, "", "test-key")
		var job struct {
			State string
			Lease string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.State == "interrupted" {
			if job.Lease != "" {
				t.Fatal("expired hold retained lease")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("held job did not expire: %s", w.Body.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
	if w := reportControl(t, s, id, lease, "running", "Run"); w.Code != 409 {
		t.Fatalf("expired worker resumed: %d %s", w.Code, w.Body.String())
	}
}
