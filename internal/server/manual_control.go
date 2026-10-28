package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

const (
	manualStateQueued      = "queued"
	manualStateAssigned    = "assigned"
	manualStateRunning     = "running"
	manualStateCanceling   = "canceling"
	manualStateCompleted   = "completed"
	manualStateFailed      = "failed"
	manualStateCanceled    = "canceled"
	manualStateInterrupted = "interrupted"
)

type manualControlRequest struct {
	Action     string  `json:"action"`
	Confirmed  bool    `json:"confirmed"`
	Axis       string  `json:"axis,omitempty"`
	DistanceMM float64 `json:"distance_mm,omitempty"`
	FeedMMMin  float64 `json:"feed_mm_min,omitempty"`
	JobID      string  `json:"job_id,omitempty"`
}

type manualControlLease struct {
	ID                 string           `json:"id"`
	Lease              string           `json:"-"`
	DriverID           string           `json:"driver_id"`
	Action             string           `json:"action"`
	Axis               string           `json:"axis,omitempty"`
	DistanceMM         float64          `json:"distance_mm,omitempty"`
	FeedMMMin          float64          `json:"feed_mm_min,omitempty"`
	JobID              string           `json:"job_id,omitempty"`
	Bounds             *toolpath.Bounds `json:"bounds,omitempty"`
	LaserOff           bool             `json:"laser_off"`
	State              string           `json:"state"`
	Error              string           `json:"error,omitempty"`
	MaxDuration        time.Duration    `json:"-"`
	MaxDurationMS      int64            `json:"max_duration_ms"`
	Created            time.Time        `json:"created"`
	Updated            time.Time        `json:"updated"`
	ExpiresAt          time.Time        `json:"expires_at"`
	Session            string           `json:"-"`
	RequireFreshStatus bool             `json:"-"`
	RequireParserReset bool             `json:"-"`
}

type manualControlAssignment struct {
	Action        string           `json:"action"`
	Axis          string           `json:"axis,omitempty"`
	DistanceMM    float64          `json:"distance_mm,omitempty"`
	FeedMMMin     float64          `json:"feed_mm_min,omitempty"`
	JobID         string           `json:"job_id,omitempty"`
	Bounds        *toolpath.Bounds `json:"bounds,omitempty"`
	LaserOff      bool             `json:"laser_off"`
	MaxDurationMS int64            `json:"max_duration_ms"`
}

func manualCapability(action string) (string, time.Duration, bool) {
	switch action {
	case "home":
		return "manual_home", 30 * time.Second, true
	case "unlock":
		return "manual_unlock", 5 * time.Second, true
	case "jog":
		return "manual_jog", 10 * time.Second, true
	case "set_zero":
		return "manual_set_zero", 5 * time.Second, true
	case "frame":
		return "manual_frame", 60 * time.Second, true
	default:
		return "", 0, false
	}
}

func manualMotion(action string) bool {
	return action == "home" || action == "jog" || action == "frame"
}

func recordManualControllerStatus(d *Driver, manual *manualControlLease, in *pollReport) {
	if in.ControllerState == "" {
		return
	}
	if manual != nil && manual.State == manualStateInterrupted && manual.RequireFreshStatus {
		if !in.ControllerStatusFresh {
			return
		}
		manual.RequireFreshStatus = false
	}
	d.ControllerState = in.ControllerState
}

func manualAssignmentActive(state string) bool {
	switch state {
	case manualStateQueued, manualStateAssigned, manualStateRunning, manualStateCanceling, manualStateInterrupted:
		return true
	default:
		return false
	}
}

func (s *Server) createManualControl(w http.ResponseWriter, r *http.Request) {
	var in manualControlRequest
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if !driverName.MatchString(id) {
		fail(w, http.StatusBadRequest, "invalid driver ID")
		return
	}
	capability, maxDuration, ok := manualCapability(in.Action)
	if !ok {
		fail(w, http.StatusBadRequest, "unsupported manual action")
		return
	}
	if !in.Confirmed {
		fail(w, http.StatusBadRequest, "operator confirmation is required for manual control")
		return
	}
	if in.Action == "jog" {
		if err := driver.ValidateJogRequest(driver.JogRequest{Axis: in.Axis, DistanceMM: in.DistanceMM, FeedMMMin: in.FeedMMMin, Confirmed: in.Confirmed}); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	} else if in.Axis != "" || in.DistanceMM != 0 || in.FeedMMMin != 0 {
		fail(w, http.StatusBadRequest, "axis, distance, and feed are valid only for a jog")
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, http.StatusInternalServerError, "could not persist driver expiration")
		return
	}
	d, online := s.drivers[id]
	if !online {
		fail(w, http.StatusConflict, "driver is offline")
		return
	}
	if s.parserQuarantine[id] != "" {
		fail(w, http.StatusConflict, "controller parser reset is unconfirmed; worker recovery or physical controller inspection is required")
		return
	}
	if d.Busy || (s.manualControls[id] != nil && manualAssignmentActive(s.manualControls[id].State)) {
		fail(w, http.StatusConflict, "driver already has an active job or manual-control lease")
		return
	}
	if !hasCapability(d, capability) {
		fail(w, http.StatusConflict, "driver does not advertise the required manual-control capability")
		return
	}
	if manualMotion(in.Action) {
		if d.ControllerState != "Idle" {
			fail(w, http.StatusConflict, "motion requires an Idle controller")
			return
		}
	} else if in.Action == "unlock" {
		if !strings.HasPrefix(d.ControllerState, "Alarm") {
			fail(w, http.StatusConflict, "unlock is available only when the controller reports Alarm")
			return
		}
	} else if d.ControllerState != "Idle" {
		fail(w, http.StatusConflict, "setting work zero requires an Idle controller")
		return
	}

	var bounds *toolpath.Bounds
	if in.Action == "frame" {
		job, exists := s.jobs[in.JobID]
		if !exists || job.Job.Review == nil || job.Job.Review.Bounds == nil || job.Job.Review.BoundsStatus != "in_bounds" {
			fail(w, http.StatusConflict, "framing requires a job with known in-bounds geometry")
			return
		}
		if job.Job.State == "running" || activeJob(job.Job.State) {
			fail(w, http.StatusConflict, "an active job cannot be framed")
			return
		}
		copyBounds := *job.Job.Review.Bounds
		bounds = &copyBounds
	} else if in.JobID != "" {
		fail(w, http.StatusBadRequest, "job_id is valid only when framing a job")
		return
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		fail(w, http.StatusInternalServerError, "could not allocate manual-control lease")
		return
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		fail(w, http.StatusInternalServerError, "could not allocate manual-control ID")
		return
	}
	if err := s.updateParserQuarantine(id, "manual-reserved: pending terminal receipt"); err != nil {
		fail(w, http.StatusInternalServerError, "could not persist manual-control safety reservation")
		return
	}
	now := time.Now().UTC()
	record := &manualControlLease{
		ID: hex.EncodeToString(idBytes), Lease: hex.EncodeToString(secret), DriverID: id,
		Action: in.Action, Axis: in.Axis, DistanceMM: in.DistanceMM, FeedMMMin: in.FeedMMMin,
		JobID: in.JobID, Bounds: bounds, LaserOff: true, State: manualStateQueued,
		MaxDuration: maxDuration, MaxDurationMS: maxDuration.Milliseconds(), Created: now,
		Updated: now, ExpiresAt: now.Add(driverTTL),
	}
	s.manualControls[id] = record
	d.Busy = true
	s.drivers[id] = d
	respond(w, http.StatusCreated, record)
}

func (s *Server) getManualControl(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	record := s.manualControls[id]
	var copyRecord manualControlLease
	if record != nil {
		copyRecord = *record
	}
	s.mu.Unlock()
	if record == nil {
		fail(w, http.StatusNotFound, "manual-control assignment not found")
		return
	}
	respond(w, http.StatusOK, copyRecord)
}

func (s *Server) releaseManualControl(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirmed bool `json:"confirmed"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.Confirmed {
		fail(w, http.StatusBadRequest, "operator confirmation is required to release a manual-control lease")
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.manualControls[id]
	if record == nil {
		if s.parserQuarantine[id] != "" {
			fail(w, http.StatusConflict, "controller parser reset is unconfirmed; worker recovery or physical controller inspection is required")
			return
		}
		fail(w, http.StatusNotFound, "manual-control assignment not found")
		return
	}
	d, online := s.drivers[id]
	if !online {
		fail(w, http.StatusConflict, "driver is offline; inspect the machine before releasing its manual-control lease")
		return
	}
	if record.State == manualStateQueued && strings.HasPrefix(s.parserQuarantine[id], "manual-reserved:") {
		if err := s.updateParserQuarantine(id, ""); err != nil {
			fail(w, http.StatusInternalServerError, "could not persist manual-control release")
			return
		}
		delete(s.manualControls, id)
		d.Busy = false
		s.drivers[id] = d
		respond(w, http.StatusOK, map[string]string{"state": "released"})
		return
	}
	if manualAssignmentActive(record.State) && record.State != manualStateQueued && record.State != manualStateInterrupted {
		record.State = manualStateCanceling
		record.Updated = time.Now().UTC()
		record.ExpiresAt = record.Updated.Add(driverTTL)
		d.Busy = true
		s.drivers[id] = d
		respond(w, http.StatusAccepted, record)
		return
	}
	if s.parserQuarantine[id] != "" {
		fail(w, http.StatusConflict, "controller parser reset is unconfirmed; worker recovery or physical controller inspection is required")
		return
	}
	if record.State == manualStateInterrupted && record.RequireParserReset {
		fail(w, http.StatusConflict, "controller parser reset was not confirmed; inspect or restart the controller before releasing its manual-control lease")
		return
	}
	if record.State == manualStateInterrupted && (record.RequireFreshStatus || d.ControllerState != "Idle") {
		fail(w, http.StatusConflict, "interrupted manual control requires a fresh Idle status before release")
		return
	}
	delete(s.manualControls, id)
	d.Busy = false
	for _, job := range s.jobs {
		if job.DriverID == id && activeJob(job.State) {
			d.Busy = true
			break
		}
	}
	s.drivers[id] = d
	respond(w, http.StatusOK, map[string]string{"state": "released"})
}

func manualWorkerAssignment(record *manualControlLease) manualControlAssignment {
	return manualControlAssignment{
		Action: record.Action, Axis: record.Axis, DistanceMM: record.DistanceMM,
		FeedMMMin: record.FeedMMMin, JobID: record.JobID, Bounds: record.Bounds,
		LaserOff: record.LaserOff, MaxDurationMS: record.MaxDurationMS,
	}
}

func manualReceiptError(record *manualControlLease, in *pollReport) error {
	if record == nil || in.ManualLease == "" || in.ManualLease != record.Lease {
		return errors.New("stale or invalid manual-control lease")
	}
	if record.Session == "" || in.Session != record.Session {
		return errors.New("manual-control lease belongs to another worker session")
	}
	if record.State != manualStateAssigned && record.State != manualStateRunning && record.State != manualStateCanceling && record.State != in.ManualState {
		return errors.New("manual-control lease is not assigned to a worker")
	}
	if len(in.ManualError) > 1000 {
		return errors.New("manual-control error is too long")
	}
	return nil
}
