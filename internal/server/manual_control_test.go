package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestManualJogAssignmentRequiresCapabilityConfirmationAndExclusiveLease(t *testing.T) {
	s := openServer(t)
	registerManualTestDriver(t, s, "Idle", []string{"manual_jog"})

	jobResponse := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"queued","gcode":"M5"}`, "test-key")
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(jobResponse.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}

	manualResponse := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"jog","confirmed":true,"axis":"X","distance_mm":2,"feed_mm_min":300}`, "test-key")
	if manualResponse.Code != http.StatusCreated {
		t.Fatalf("create manual jog assignment: %d %s", manualResponse.Code, manualResponse.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Action string `json:"action"`
		State  string `json:"state"`
	}
	if err := json.Unmarshal(manualResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Action != "jog" || created.State != "queued" {
		t.Fatalf("created manual assignment = %s", manualResponse.Body.String())
	}

	start := request(t, s, http.MethodPost, "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if start.Code != http.StatusConflict {
		t.Fatalf("job start while manual lease active = %d %s; want conflict", start.Code, start.Body.String())
	}

	pickup := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["manual_jog"],"controller_state":"Idle"}`, "test-key")
	if pickup.Code != http.StatusOK || !strings.Contains(pickup.Body.String(), `"command":"idle"`) || strings.Contains(pickup.Body.String(), `"manual"`) {
		t.Fatalf("manual assignment was dispatched before worker support: %d %s", pickup.Code, pickup.Body.String())
	}
	state := request(t, s, http.MethodGet, "/api/v1/drivers/bench/manual", "", "test-key")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"state":"queued"`) {
		t.Fatalf("manual assignment did not remain dormant: %d %s", state.Code, state.Body.String())
	}

	start = request(t, s, http.MethodPost, "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if start.Code != http.StatusConflict {
		t.Fatalf("job start while manual assignment awaits worker support = %d %s", start.Code, start.Body.String())
	}
}

func TestManualJogRejectsMissingCapabilityConfirmationAndIdleState(t *testing.T) {
	t.Run("missing capability", func(t *testing.T) {
		s := openServer(t)
		registerManualTestDriver(t, s, "Idle", []string{"pause_resume"})
		got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"jog","confirmed":true,"axis":"X","distance_mm":1,"feed_mm_min":100}`, "test-key")
		if got.Code != http.StatusConflict {
			t.Fatalf("manual jog without capability = %d %s", got.Code, got.Body.String())
		}
	})

	t.Run("missing confirmation", func(t *testing.T) {
		s := openServer(t)
		registerManualTestDriver(t, s, "Idle", []string{"manual_jog"})
		got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"jog","confirmed":false,"axis":"X","distance_mm":1,"feed_mm_min":100}`, "test-key")
		if got.Code != http.StatusBadRequest {
			t.Fatalf("unconfirmed manual jog = %d %s", got.Code, got.Body.String())
		}
	})

	t.Run("non-idle controller", func(t *testing.T) {
		s := openServer(t)
		registerManualTestDriver(t, s, "Run", []string{"manual_jog"})
		got := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"jog","confirmed":true,"axis":"X","distance_mm":1,"feed_mm_min":100}`, "test-key")
		if got.Code != http.StatusConflict {
			t.Fatalf("manual jog while running = %d %s", got.Code, got.Body.String())
		}
	})
}

func registerManualTestDriver(t *testing.T, s http.Handler, state string, capabilities []string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"session": "owner", "controller_state": state, "capabilities": capabilities})
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", string(body), "test-key")
	if response.Code != http.StatusOK {
		t.Fatalf("register manual test driver: %d %s", response.Code, response.Body.String())
	}
}
