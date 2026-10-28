package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
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
	var assignment struct {
		Command string `json:"command"`
		Lease   string `json:"lease"`
		Manual  struct {
			Action        string  `json:"action"`
			Axis          string  `json:"axis"`
			DistanceMM    float64 `json:"distance_mm"`
			FeedMMMin     float64 `json:"feed_mm_min"`
			LaserOff      bool    `json:"laser_off"`
			MaxDurationMS int64   `json:"max_duration_ms"`
		} `json:"manual"`
		Job json.RawMessage `json:"job"`
	}
	if err := json.Unmarshal(pickup.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	if pickup.Code != http.StatusOK || assignment.Command != "manual" || assignment.Lease == "" || assignment.Manual.Action != "jog" || assignment.Manual.Axis != "X" || assignment.Manual.DistanceMM != 2 || assignment.Manual.FeedMMMin != 300 || !assignment.Manual.LaserOff || assignment.Manual.MaxDurationMS != 10000 {
		t.Fatalf("manual pickup = %d %s", pickup.Code, pickup.Body.String())
	}
	if len(assignment.Job) != 0 || strings.Contains(pickup.Body.String(), `"gcode"`) {
		t.Fatalf("manual assignment included job G-code: %s", pickup.Body.String())
	}
	state := request(t, s, http.MethodGet, "/api/v1/drivers/bench/manual", "", "test-key")
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), `"state":"assigned"`) || strings.Contains(state.Body.String(), `"lease":"`) {
		t.Fatalf("manual assignment state or secret handling: %d %s", state.Code, state.Body.String())
	}
	progress := `{"session":"owner","capabilities":["manual_jog"],"controller_state":"Run","manual_lease":"` + assignment.Lease + `","manual_state":"running"}`
	running := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", progress, "test-key")
	if running.Code != http.StatusOK || !strings.Contains(running.Body.String(), `"command":"manual_running"`) || strings.Contains(running.Body.String(), `"gcode"`) {
		t.Fatalf("manual progress poll: %d %s", running.Code, running.Body.String())
	}
	terminal := `{"session":"owner","capabilities":["manual_jog"],"controller_state":"Idle","manual_lease":"` + assignment.Lease + `","manual_state":"completed"}`
	completed := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", terminal, "test-key")
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"command":"idle"`) {
		t.Fatalf("manual completion poll: %d %s", completed.Code, completed.Body.String())
	}

	start = request(t, s, http.MethodPost, "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if start.Code != http.StatusOK {
		t.Fatalf("job start after manual completion = %d %s", start.Code, start.Body.String())
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

func TestManualCancelRequiresResetProofAndFreshIdleBeforeReleaseAfterUnsafeStop(t *testing.T) {
	s := openServer(t)
	registerManualTestDriver(t, s, "Idle", []string{"manual_jog"})
	created := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"jog","confirmed":true,"axis":"X","distance_mm":2,"feed_mm_min":300}`, "test-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("create jog: %d %s", created.Code, created.Body.String())
	}
	pickup := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["manual_jog"],"controller_state":"Idle"}`, "test-key")
	var assignment struct {
		Lease string `json:"lease"`
	}
	if err := json.Unmarshal(pickup.Body.Bytes(), &assignment); err != nil || assignment.Lease == "" {
		t.Fatalf("pickup: %s (%v)", pickup.Body.String(), err)
	}
	cancel := request(t, s, http.MethodDelete, "/api/v1/drivers/bench/manual", `{"confirmed":true}`, "test-key")
	if cancel.Code != http.StatusAccepted || !strings.Contains(cancel.Body.String(), `"state":"canceling"`) {
		t.Fatalf("cancel assigned jog: %d %s", cancel.Code, cancel.Body.String())
	}
	running := `{"session":"owner","capabilities":["manual_jog"],"controller_state":"Run","manual_lease":"` + assignment.Lease + `","manual_state":"running"}`
	response := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", running, "test-key")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"command":"manual_cancel"`) {
		t.Fatalf("manual cancel command: %d %s", response.Code, response.Body.String())
	}
	terminal := `{"session":"owner","capabilities":["manual_jog"],"controller_state":"Run","manual_lease":"` + assignment.Lease + `","manual_state":"canceled"}`
	response = request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", terminal, "test-key")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"command":"idle"`) || !strings.Contains(response.Body.String(), `"read_parser_reset":true`) {
		t.Fatalf("unsafe stop receipt: %d %s", response.Code, response.Body.String())
	}
	state := request(t, s, http.MethodGet, "/api/v1/drivers/bench/manual", "", "test-key")
	if !strings.Contains(state.Body.String(), `"state":"interrupted"`) {
		t.Fatalf("unsafe cancellation released lease: %s", state.Body.String())
	}
	job := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"blocked.gcode","gcode":"M5"}`, "test-key")
	var queued struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(job.Body.Bytes(), &queued)
	start := request(t, s, http.MethodPost, "/api/v1/jobs/"+queued.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if start.Code != http.StatusConflict {
		t.Fatalf("job start before fresh Idle = %d %s", start.Code, start.Body.String())
	}
	request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["manual_jog"],"controller_state":"Idle","controller_status_fresh":true,"controller_parser_reset_fresh":true}`, "test-key")
	released := request(t, s, http.MethodDelete, "/api/v1/drivers/bench/manual", `{"confirmed":true}`, "test-key")
	if released.Code != http.StatusOK {
		t.Fatalf("release after Idle = %d %s", released.Code, released.Body.String())
	}
	start = request(t, s, http.MethodPost, "/api/v1/jobs/"+queued.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if start.Code != http.StatusOK {
		t.Fatalf("job start after inspected release = %d %s", start.Code, start.Body.String())
	}
}

func TestUnconfirmedParserResetKeepsManualLeaseBlockedAfterFreshIdle(t *testing.T) {
	s := openServer(t)
	registerManualTestDriver(t, s, "Idle", []string{"manual_set_zero"})
	created := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"set_zero","confirmed":true}`, "test-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("create zero: %d %s", created.Code, created.Body.String())
	}
	pickup := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["manual_set_zero"],"controller_state":"Idle"}`, "test-key")
	var assignment struct {
		Lease string `json:"lease"`
	}
	if err := json.Unmarshal(pickup.Body.Bytes(), &assignment); err != nil || assignment.Lease == "" {
		t.Fatalf("pickup: %s (%v)", pickup.Body.String(), err)
	}
	terminal := `{"session":"owner","capabilities":["manual_set_zero"],"controller_state":"Idle","controller_status_fresh":true,"manual_lease":"` + assignment.Lease + `","manual_state":"failed","manual_error":"driver: manual command parser reset was not confirmed"}`
	response := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", terminal, "test-key")
	if response.Code != http.StatusOK {
		t.Fatalf("unconfirmed parser reset receipt: %d %s", response.Code, response.Body.String())
	}
	state := request(t, s, http.MethodGet, "/api/v1/drivers/bench/manual", "", "test-key")
	if !strings.Contains(state.Body.String(), `"state":"interrupted"`) {
		t.Fatalf("unconfirmed parser reset lease = %s; want interrupted", state.Body.String())
	}
	request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["manual_set_zero"],"controller_state":"Idle","controller_status_fresh":true}`, "test-key")
	released := request(t, s, http.MethodDelete, "/api/v1/drivers/bench/manual", `{"confirmed":true}`, "test-key")
	if released.Code != http.StatusConflict || !strings.Contains(released.Body.String(), "parser reset is unconfirmed") {
		t.Fatalf("release after fresh Idle without parser reset proof = %d %s; want conflict", released.Code, released.Body.String())
	}
}

func TestQueuedManualLeaseCanBeReleasedAndClearsReservation(t *testing.T) {
	s := openServer(t)
	registerManualTestDriver(t, s, "Idle", []string{"manual_set_zero", "manual_unlock"})
	created := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"set_zero","confirmed":true}`, "test-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("create queued zero: %d %s", created.Code, created.Body.String())
	}
	released := request(t, s, http.MethodDelete, "/api/v1/drivers/bench/manual", `{"confirmed":true}`, "test-key")
	if released.Code != http.StatusOK {
		t.Fatalf("release queued manual lease: %d %s", released.Code, released.Body.String())
	}
	created = request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"set_zero","confirmed":true}`, "test-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("create manual lease after queued release: %d %s", created.Code, created.Body.String())
	}
}

func TestParserResetQuarantineSurvivesRestartAndRequiresWorkerResetProof(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	registerManualTestDriver(t, s, "Idle", []string{"manual_set_zero", "manual_unlock"})
	created := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"set_zero","confirmed":true}`, "test-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("create zero: %d %s", created.Code, created.Body.String())
	}
	pickup := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["manual_set_zero","manual_unlock"],"controller_state":"Idle"}`, "test-key")
	var assignment struct {
		Lease string `json:"lease"`
	}
	if err := json.Unmarshal(pickup.Body.Bytes(), &assignment); err != nil || assignment.Lease == "" {
		t.Fatalf("pickup: %s (%v)", pickup.Body.String(), err)
	}
	// Simulate a server crash after assignment but before the worker can submit
	// its terminal receipt. The durable reservation must already exist.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatalf("reopen server with parser quarantine: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	registerManualTestDriver(t, s, "Idle", []string{"manual_set_zero", "manual_unlock"})
	job := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"queued.gcode","gcode":"M5"}`, "test-key")
	var queued struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(job.Body.Bytes(), &queued); err != nil || queued.ID == "" {
		t.Fatalf("create queued job: %s (%v)", job.Body.String(), err)
	}
	start := request(t, s, http.MethodPost, "/api/v1/jobs/"+queued.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if start.Code != http.StatusConflict || !strings.Contains(start.Body.String(), "parser reset is unconfirmed") {
		t.Fatalf("job start while parser quarantined = %d %s; want conflict", start.Code, start.Body.String())
	}
	manual := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"unlock","confirmed":true}`, "test-key")
	if manual.Code != http.StatusConflict || !strings.Contains(manual.Body.String(), "parser reset is unconfirmed") {
		t.Fatalf("manual action while parser quarantined = %d %s; want conflict", manual.Code, manual.Body.String())
	}
	if released := request(t, s, http.MethodDelete, "/api/v1/drivers/bench/manual", `{"confirmed":true}`, "test-key"); released.Code != http.StatusConflict || !strings.Contains(released.Body.String(), "parser reset is unconfirmed") {
		t.Fatalf("release after server restart without parser proof = %d %s; want conflict", released.Code, released.Body.String())
	}
	probe := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", `{"session":"owner","capabilities":["manual_set_zero","manual_unlock"],"controller_state":"Idle"}`, "test-key")
	if !strings.Contains(probe.Body.String(), `"read_parser_reset":true`) {
		t.Fatalf("quarantined worker command = %s; want reset recovery", probe.Body.String())
	}
	proof := `{"session":"owner","capabilities":["manual_set_zero","manual_unlock"],"controller_state":"Alarm","controller_status_fresh":true,"controller_parser_reset_fresh":true}`
	if response := request(t, s, http.MethodPost, "/api/v1/drivers/bench/poll", proof, "test-key"); response.Code != http.StatusOK {
		t.Fatalf("confirmed parser reset report: %d %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "parser-quarantine.json"))
	if err != nil || strings.Contains(string(data), "bench") {
		t.Fatalf("parser quarantine after confirmed reset = %s, %v; want cleared", data, err)
	}
	manual = request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"unlock","confirmed":true}`, "test-key")
	if manual.Code != http.StatusCreated {
		t.Fatalf("manual unlock after reset proof = %d %s; want recoverable Alarm flow", manual.Code, manual.Body.String())
	}
}
