package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExpiredManualLeaseRequiresFreshIdleStatusBeforeRelease(t *testing.T) {
	s := &Server{
		drivers: map[string]Driver{
			"bench": {ID: "bench", ControllerState: "Idle", LastSeen: time.Now().Add(-2 * driverTTL), Busy: true},
		},
		manualControls: map[string]*manualControlLease{
			"bench": {ID: "manual-id", DriverID: "bench", Action: "home", State: manualStateRunning, Session: "old-session"},
		},
		jobs: map[string]jobRecord{},
	}
	s.metrics = newServerMetrics(s)
	if err := s.expire(); err != nil {
		t.Fatalf("expire worker lease: %v", err)
	}
	manual := s.manualControls["bench"]
	if manual.State != manualStateInterrupted || !manual.RequireFreshStatus {
		t.Fatalf("expired manual lease = %#v; want interrupted with fresh status required", manual)
	}

	// A reconnect can initially report the cached Idle value from before the
	// manual action. It must not satisfy the fresh-status requirement.
	driver := Driver{ID: "bench", ControllerState: "Idle", LastSeen: time.Now(), Busy: true}
	recordManualControllerStatus(&driver, manual, &pollReport{ControllerState: "Idle"})
	s.drivers["bench"] = driver
	if response := s.idleCommand("bench"); response["read_status"] != true {
		t.Fatalf("interrupted lease with cached Idle got idle command %#v; want a fresh status probe", response)
	}
	if got := releaseManualRequest(s, "bench"); got.Code != 409 {
		t.Fatalf("release with cached Idle = %d %s; want conflict", got.Code, got.Body.String())
	}

	// A failed probe leaves the requirement in force; a fresh non-Idle state
	// clears the probe requirement but still cannot release the lease.
	recordManualControllerStatus(&driver, manual, &pollReport{})
	if !manual.RequireFreshStatus {
		t.Fatal("failed status probe cleared freshness requirement")
	}
	recordManualControllerStatus(&driver, manual, &pollReport{ControllerState: "Run", ControllerStatusFresh: true})
	if manual.RequireFreshStatus || driver.ControllerState != "Run" {
		t.Fatalf("fresh Run status was not recorded: manual=%#v driver=%#v", manual, driver)
	}
	s.drivers["bench"] = driver
	if got := releaseManualRequest(s, "bench"); got.Code != 409 {
		t.Fatalf("release with fresh Run = %d %s; want conflict", got.Code, got.Body.String())
	}

	recordManualControllerStatus(&driver, manual, &pollReport{ControllerState: "Idle", ControllerStatusFresh: true})
	s.drivers["bench"] = driver
	if got := releaseManualRequest(s, "bench"); got.Code != 200 || !strings.Contains(got.Body.String(), "released") {
		t.Fatalf("release after fresh Idle = %d %s; want success", got.Code, got.Body.String())
	}
}

func releaseManualRequest(s *Server, id string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("DELETE", "/api/v1/drivers/"+id+"/manual", strings.NewReader(`{"confirmed":true}`))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	s.releaseManualControl(w, r)
	return w
}
