package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

const driverTTL = 10 * time.Second

var driverName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// Driver describes an online worker. Sessions prevent duplicate worker ownership.
type Driver struct {
	Capabilities    []string  `json:"capabilities,omitempty"`
	ControllerState string    `json:"controller_state,omitempty"`
	ID              string    `json:"id"`
	LastSeen        time.Time `json:"last_seen"`
	Busy            bool      `json:"busy"`
	Session         string    `json:"-"`
	CredentialID    string    `json:"-"`
	// Controller settings last read by the worker with `$$` (see controller_settings.go).
	ControllerSettings *driver.Settings `json:"controller_settings,omitempty"`
	SettingsReadAt     *time.Time       `json:"settings_read_at,omitempty"`
	SettingsError      string           `json:"settings_error,omitempty"`
	settingsRequested  time.Time
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		fail(w, 400, "invalid request JSON")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		fail(w, 400, "expected one JSON object")
		return false
	}
	return true
}
func (s *Server) recoverInterrupted() error {
	jobs := s.copyJobs()
	changed := false
	for id, j := range jobs {
		if activeJob(j.State) {
			j.State = "interrupted"
			j.Error = "server restarted; inspect machine before creating a new job"
			j.Lease = ""
			j.Execution = markExecutionUnknown(j.Execution)
			jobs[id] = j
			changed = true
		}
	}
	if changed {
		return s.save(jobs)
	}
	return nil
}

// expire is called under the mutation lock. Offline work is never reassigned.
func (s *Server) expire() error {
	var jobs map[string]jobRecord
	expired := []string{}
	for id, d := range s.drivers {
		if time.Since(d.LastSeen) > driverTTL {
			for jid, j := range s.jobs {
				if j.DriverID == id && activeJob(j.State) {
					if jobs == nil {
						jobs = s.copyJobs()
					}
					j.State = "interrupted"
					j.Error = "driver heartbeat expired; inspect machine"
					j.Lease = ""
					j.Execution = markExecutionUnknown(j.Execution)
					jobs[jid] = j
				}
			}
			expired = append(expired, id)
		}
	}
	if jobs != nil {
		if err := s.save(jobs); err != nil {
			return err
		}
	}
	for _, id := range expired {
		delete(s.drivers, id)
		s.metrics.driverExpires.Inc()
	}
	return nil
}
func (s *Server) listDrivers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return
	}
	drivers := make([]Driver, 0, len(s.drivers))
	for _, d := range s.drivers {
		drivers = append(drivers, d)
	}
	respond(w, 200, drivers)
}

const boundsOverrideRequired = "program bounds are unknown or outside the selected profile; explicit bounds_override confirmation is required"

// boundsBlocked reports whether an uploaded job needs an explicit bounds
// override before dispatch. Callers hold the server mutex.
func (s *Server) boundsBlocked(j jobRecord, override bool) bool {
	if j.Provenance != nil || override {
		return false
	}
	review := s.currentReview(j.Job)
	return review == nil || review.BoundsStatus != "in_bounds"
}

func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DriverID       string `json:"driver_id"`
		Confirmed      bool   `json:"confirmed"`
		BoundsOverride bool   `json:"bounds_override"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !in.Confirmed {
		fail(w, 400, "operator must confirm supervision and machine readiness")
		return
	}
	record, ok := s.lookup(w, r.PathValue("id"))
	if !ok {
		return
	}
	if record.State != "queued" {
		fail(w, 409, "only queued jobs can start")
		return
	}
	s.mu.Lock()
	blocked := s.boundsBlocked(record, in.BoundsOverride)
	s.mu.Unlock()
	if blocked {
		fail(w, 409, boundsOverrideRequired)
		return
	}
	gcode, ok := s.program(w, record)
	if !ok {
		return
	}
	segments, err := s.preflight(r.Context(), gcode)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return
	}
	j, ok := s.jobs[r.PathValue("id")]
	if !ok {
		fail(w, 404, "job not found")
		return
	}
	if j.State != "queued" {
		fail(w, 409, "only queued jobs can start")
		return
	}
	if s.boundsBlocked(j, in.BoundsOverride) {
		fail(w, 409, boundsOverrideRequired)
		return
	}
	d, ok := s.drivers[in.DriverID]
	if !ok || d.Busy {
		fail(w, 409, "driver is offline or busy")
		return
	}
	if message := settingsBlock(j, d); message != "" {
		fail(w, 409, message)
		return
	}
	lease := make([]byte, 32)
	if _, err := rand.Read(lease); err != nil {
		fail(w, 500, "could not allocate lease")
		return
	}
	j.State = "running"
	j.DriverID = d.ID
	j.Lease = hex.EncodeToString(lease)
	j.segments = segments
	jobs := s.copyJobs()
	jobs[j.ID] = j
	if err := s.save(jobs); err != nil {
		fail(w, 500, "could not persist assignment")
		return
	}
	d.Busy = true
	s.drivers[d.ID] = d
	respond(w, 200, j.Job)
}
func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[r.PathValue("id")]
	if !ok {
		fail(w, 404, "job not found")
		return
	}
	switch j.State {
	case "queued":
		j.State = "canceled"
	case "running", "pausing", "paused", "resuming", "canceling":
		j.State = "canceling"
	default:
		fail(w, 409, "job is already terminal")
		return
	}
	jobs := s.copyJobs()
	jobs[j.ID] = j
	if err := s.save(jobs); err != nil {
		fail(w, 500, "could not persist cancellation")
		return
	}
	respond(w, 200, j.Job)
}
func (s *Server) pollDriver(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !driverName.MatchString(id) {
		fail(w, 400, "invalid driver ID")
		return
	}
	var in pollReport
	if !decode(w, r, &in) {
		return
	}
	if in.Session == "" || len(in.Session) > 128 {
		fail(w, 400, "worker session is required")
		return
	}
	assignment, needsProgram, ok := s.pollLocked(w, id, requestPrincipal(r).ID, &in)
	if !ok {
		return
	}
	if assignment == nil {
		respond(w, 200, s.idleCommand(id))
		return
	}
	if needsProgram {
		// A worker picking up an assignment receives the program once. Progress
		// acknowledgements carry only metadata so polling stays cheap.
		gcode, ok := s.program(w, assignment.record)
		if !ok {
			return
		}
		assignment.job.Gcode = gcode
	}
	respond(w, 200, map[string]any{"command": assignment.command, "job": assignment.job, "lease": assignment.job.Lease})
}

// pollReport is one worker poll: session metadata plus optional job progress.
type pollReport struct {
	Capabilities    []string            `json:"capabilities"`
	ControllerState string              `json:"controller_state"`
	Session         string              `json:"session"`
	JobID           string              `json:"job_id"`
	Lease           string              `json:"lease"`
	State           string              `json:"state"`
	Acked           int                 `json:"acked"`
	Total           int                 `json:"total"`
	Error           string              `json:"error"`
	Execution       *toolpath.Execution `json:"execution"`
	// Controller settings read with `$$` (see controller_settings.go).
	ControllerSettings *driver.Settings `json:"controller_settings"`
	SettingsReadAt     *time.Time       `json:"settings_read_at"`
	SettingsError      string           `json:"settings_error"`
}

type pollAssignment struct {
	command string
	job     workerJob
	record  jobRecord
}

// pollLocked applies one worker report under the mutex. Progress-only reports
// update memory; state transitions are persisted before acknowledgement.
func (s *Server) pollLocked(w http.ResponseWriter, id, credentialID string, in *pollReport) (*pollAssignment, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, credentialActive := s.credentials[credentialID]
	if !credentialActive || credential.Revoked {
		fail(w, http.StatusUnauthorized, "authentication required")
		return nil, false, false
	}
	if credential.Role == "worker" && !containsDriver(credential.DriverIDs, id) {
		fail(w, http.StatusForbidden, "worker credential is not authorized for this driver ID")
		return nil, false, false
	}
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return nil, false, false
	}
	d, ok := s.drivers[id]
	if ok && d.CredentialID != credentialID {
		fail(w, 409, "driver ID is already owned by another credential")
		return nil, false, false
	}
	if ok && d.Session != in.Session {
		fail(w, 409, "driver ID already owned by another session")
		return nil, false, false
	}
	if len(in.Capabilities) > 16 || len(in.ControllerState) > 32 {
		fail(w, 400, "invalid controller metadata")
		return nil, false, false
	}
	for _, capability := range in.Capabilities {
		if !driverName.MatchString(capability) {
			fail(w, 400, "invalid driver capability")
			return nil, false, false
		}
	}
	if !validSettingsReport(in) {
		fail(w, 400, "invalid controller settings report")
		return nil, false, false
	}
	recordSettings(&d, in)
	if in.Capabilities != nil {
		d.Capabilities = append([]string(nil), in.Capabilities...)
	}
	if in.ControllerState != "" {
		d.ControllerState = in.ControllerState
	}
	d.ID = id
	d.Session = in.Session
	d.CredentialID = credentialID
	d.LastSeen = time.Now().UTC()
	if in.JobID != "" {
		j, exists := s.jobs[in.JobID]
		if !exists || j.DriverID != id || j.Lease == "" || j.Lease != in.Lease {
			fail(w, 409, "stale or invalid job lease")
			return nil, false, false
		}
		previousState := j.State
		if !activeJob(j.State) {
			// Repeated terminal receipts are idempotent after response loss.
			if j.State != in.State {
				fail(w, 409, "job is already terminal")
				return nil, false, false
			}
		} else {
			if in.Acked < j.Acked || in.Total < 0 || in.Acked > in.Total || in.Acked < 0 || (j.Total != 0 && j.Total != in.Total) {
				fail(w, 400, "invalid progress")
				return nil, false, false
			}
			if in.Execution != nil && !validExecution(*in.Execution, j.segments, j.Execution, in.State) {
				fail(w, 400, "invalid execution telemetry")
				return nil, false, false
			}
			switch in.State {
			case "pausing", "resuming":
				if !hasCapability(d, "pause_resume") {
					fail(w, 400, "worker does not support pause and resume")
					return nil, false, false
				}
			case "running":
				if j.State == "resuming" && (in.ControllerState == "Run" || in.ControllerState == "Idle") {
					j.State = "running"
				}
			case "paused":
				if !hasCapability(d, "pause_resume") || (in.ControllerState != "Hold:0" && in.ControllerState != "Idle") {
					fail(w, 400, "paused requires confirmed controller hold or Idle")
					return nil, false, false
				}
				if j.State != "canceling" && j.State != "resuming" {
					j.State = "paused"
				}
			case "completed":
				if in.Total == 0 || in.Acked != in.Total || (hasCapability(d, "pause_resume") && in.ControllerState != "Idle") {
					fail(w, 409, "completion requires all lines acknowledged and controller Idle")
					return nil, false, false
				}
				j.State = "completed"
			case "failed":
				j.State = "failed"
			case "canceled":
				if j.State != "canceling" {
					fail(w, 409, "cancellation was not requested")
					return nil, false, false
				}
				j.State = "canceled"
			default:
				fail(w, 400, "invalid worker state")
				return nil, false, false
			}
			j.ControllerState = in.ControllerState
			j.Acked = in.Acked
			j.Total = in.Total
			j.Error = in.Error
			if in.Execution != nil {
				execution := *in.Execution
				j.Execution = &execution
			}
			if len(j.Error) > 1000 {
				j.Error = j.Error[:1000]
			}
			if j.State == previousState {
				// Progress-only: keep it in memory. Recovery after a crash marks
				// the job interrupted anyway, so the last transition is enough.
				s.jobs[j.ID] = j
				s.progressDirty = true
			} else {
				jobs := s.copyJobs()
				jobs[j.ID] = j
				if err := s.save(jobs); err != nil {
					fail(w, 500, "could not persist progress")
					return nil, false, false
				}
			}
		}
	}
	d.Busy = false
	for _, j := range s.jobs {
		if j.DriverID == id && activeJob(j.State) {
			d.Busy = true
			s.drivers[id] = d
			command := "run"
			switch j.State {
			case "canceling":
				command = "cancel"
			case "pausing", "paused":
				command = "pause"
			case "resuming":
				command = "resume"
			}
			// Only an assignment pickup (not a progress report for this job)
			// carries the program; the current worker reads it from the pickup.
			needsProgram := in.JobID != j.ID && command != "cancel"
			return &pollAssignment{command: command, job: workerJob{Job: j.Job, Lease: j.Lease, ExpectedSettings: expectedSettings(j.Job)}, record: j}, needsProgram, true
		}
	}
	s.drivers[id] = d
	return nil, false, true
}
