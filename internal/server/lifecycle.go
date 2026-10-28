package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/jobtransfer"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

const driverTTL = 10 * time.Second

var driverName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (s *Server) updateParserQuarantine(id, reason string) error {
	next := make(map[string]string, len(s.parserQuarantine)+1)
	for key, value := range s.parserQuarantine {
		next[key] = value
	}
	if reason == "" {
		delete(next, id)
	} else {
		next[id] = reason
	}
	published, err := durableJSON(filepath.Join(s.dir, "parser-quarantine.json"), next)
	if published && err != nil {
		s.failStorage(err)
	}
	if err != nil {
		return err
	}
	s.parserQuarantine = next
	return nil
}

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
	CameraAvailable    bool             `json:"camera_available"`
	settingsRequested  time.Time
	statusRequested    time.Time
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
		return s.save(jobs, transitionCause{source: "server", action: "restart"})
	}
	return nil
}

// expire is called under the mutation lock. Offline work is never reassigned.
func (s *Server) expire() error {
	var jobs map[string]jobRecord
	expired := []string{}
	for id, d := range s.drivers {
		if time.Since(d.LastSeen) > driverTTL {
			if manual := s.manualControls[id]; manual != nil && manualAssignmentActive(manual.State) {
				manual.State = manualStateInterrupted
				manual.RequireFreshStatus = true
				manual.Error = "worker lease expired; inspect the machine before releasing manual control"
				manual.Updated = time.Now().UTC()
			}
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
		// Drivers are removed only after the save, so events still name the
		// expired session.
		if err := s.save(jobs, transitionCause{source: "server", action: "heartbeat_expired"}); err != nil {
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
		_, d.CameraAvailable = s.cameras[d.ID]
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

func legacyAssignmentExceedsLimit(job Job, gcode, lease string) bool {
	// Use the longest control command and state name so a later pause/resume
	// assignment cannot grow beyond the response an old worker can read.
	job.State = "resuming"
	response := map[string]any{
		"command":  "resume",
		"job":      workerJob{Job: job, Lease: lease, Gcode: gcode},
		"lease":    lease,
		"protocol": 1,
	}
	encoded, err := json.Marshal(response)
	return err != nil || len(encoded)+1 > jobtransfer.MaxLegacyAssignmentBytes
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
	if record.Size > jobtransfer.MaxProgramBytes {
		fail(w, http.StatusRequestEntityTooLarge, "job program exceeds the 4 MiB worker transfer maximum")
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
	if !ok {
		fail(w, 409, "driver is offline or busy")
		return
	}
	if s.parserQuarantine[d.ID] != "" {
		fail(w, http.StatusConflict, "controller parser reset is unconfirmed; worker recovery or physical controller inspection is required")
		return
	}
	if d.Busy {
		fail(w, 409, "driver is offline or busy")
		return
	}
	if manual := s.manualControls[d.ID]; manual != nil && manualAssignmentActive(manual.State) {
		fail(w, http.StatusConflict, "driver has an active or unresolved manual-control lease")
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
	if !hasCapability(d, jobtransfer.Capability) && legacyAssignmentExceedsLimit(j.Job, gcode, j.Lease) {
		fail(w, http.StatusRequestEntityTooLarge, "job assignment exceeds the 8 MiB legacy worker limit; upgrade the worker or reduce the job")
		return
	}
	jobs := s.copyJobs()
	jobs[j.ID] = j
	if err := s.save(jobs, operatorCause(r, "start")); err != nil {
		fail(w, 500, "could not persist assignment")
		return
	}
	d.Busy = true
	s.drivers[d.ID] = d
	respond(w, 200, s.jobs[j.ID].Job)
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
	if err := s.save(jobs, operatorCause(r, "cancel")); err != nil {
		fail(w, 500, "could not persist cancellation")
		return
	}
	respond(w, 200, s.jobs[j.ID].Job)
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
	assignment, needsProgram, ok := s.pollLocked(w, id, workerCause(r, in.Session), &in)
	if !ok {
		return
	}
	if assignment == nil {
		respond(w, 200, s.idleCommand(id))
		return
	}
	if assignment.manual != nil {
		if assignment.command == "manual" {
			respond(w, http.StatusOK, map[string]any{"command": assignment.command, "lease": assignment.manual.Lease, "manual": manualWorkerAssignment(assignment.manual)})
		} else {
			respond(w, http.StatusOK, map[string]any{"command": assignment.command})
		}
		return
	}
	if needsProgram {
		if assignment.chunked {
			assignment.job.Content = &jobtransfer.Manifest{Size: assignment.record.Size, SHA256: assignment.record.SHA256}
		} else {
			// Legacy workers receive the complete program in the assignment.
			// Capability-negotiated workers fetch it in bounded authenticated chunks.
			gcode, ok := s.program(w, assignment.record)
			if !ok {
				return
			}
			assignment.job.Gcode = gcode
		}
	}
	protocol := 1
	if assignment.chunked {
		protocol = 2
	}
	response := map[string]any{"command": assignment.command, "job": assignment.job, "lease": assignment.job.Lease, "protocol": protocol}
	if needsProgram && !assignment.chunked {
		encoded, err := json.Marshal(response)
		if err != nil {
			fail(w, http.StatusInternalServerError, "could not encode worker assignment")
			return
		}
		if len(encoded)+1 > jobtransfer.MaxLegacyAssignmentBytes {
			fail(w, http.StatusRequestEntityTooLarge, "assignment exceeds the 8 MiB legacy worker limit; upgrade the worker or reduce the job")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append(encoded, '\n'))
		return
	}
	respond(w, http.StatusOK, response)
}

// pollReport is one worker poll: session metadata plus optional job progress.
type pollReport struct {
	Capabilities               []string            `json:"capabilities"`
	ControllerState            string              `json:"controller_state"`
	ControllerStatusFresh      bool                `json:"controller_status_fresh,omitempty"`
	ControllerParserResetFresh bool                `json:"controller_parser_reset_fresh,omitempty"`
	Session                    string              `json:"session"`
	JobID                      string              `json:"job_id"`
	Lease                      string              `json:"lease"`
	State                      string              `json:"state"`
	Acked                      int                 `json:"acked"`
	Total                      int                 `json:"total"`
	Error                      string              `json:"error"`
	Execution                  *toolpath.Execution `json:"execution"`
	// Controller settings read with `$$` (see controller_settings.go).
	ControllerSettings *driver.Settings `json:"controller_settings"`
	SettingsReadAt     *time.Time       `json:"settings_read_at"`
	SettingsError      string           `json:"settings_error"`
	ManualLease        string           `json:"manual_lease,omitempty"`
	ManualState        string           `json:"manual_state,omitempty"`
	ManualError        string           `json:"manual_error,omitempty"`
}

type pollAssignment struct {
	command string
	job     workerJob
	record  jobRecord
	chunked bool
	manual  *manualControlLease
}

type terminalReceiptFields struct {
	State           string              `json:"state"`
	Acked           int                 `json:"acked"`
	Total           int                 `json:"total"`
	ControllerState string              `json:"controller_state"`
	Error           string              `json:"error"`
	Execution       *toolpath.Execution `json:"execution"`
}

func terminalReceiptDigest(in *pollReport) string {
	encoded, _ := json.Marshal(terminalReceiptFields{
		State: in.State, Acked: in.Acked, Total: in.Total,
		ControllerState: in.ControllerState, Error: in.Error, Execution: in.Execution,
	})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func matchesTerminalReceipt(job jobRecord, in *pollReport) bool {
	if job.TerminalReceiptDigest != "" {
		return job.TerminalReceiptDigest == terminalReceiptDigest(in)
	}

	// Older persisted jobs predate receipt digests. Compare the fields that were
	// stored so their original terminal reports can still be retried safely.
	errorText := in.Error
	if len(errorText) > 1000 {
		errorText = errorText[:1000]
	}
	return job.State == in.State && job.Acked == in.Acked && job.Total == in.Total &&
		job.ControllerState == in.ControllerState && job.Error == errorText &&
		reflect.DeepEqual(job.Execution, in.Execution)
}

func sameCapabilities(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, capability := range left {
		counts[capability]++
	}
	for _, capability := range right {
		counts[capability]--
		if counts[capability] < 0 {
			return false
		}
	}
	return true
}

// pollLocked applies one worker report under the mutex. Progress-only reports
// update memory; state transitions are persisted before acknowledgement.
func (s *Server) pollLocked(w http.ResponseWriter, id string, cause transitionCause, in *pollReport) (*pollAssignment, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, credentialActive := s.credentials[cause.actor.ID]
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
	if ok && d.CredentialID != cause.actor.ID {
		fail(w, 409, "driver ID is already owned by another credential")
		return nil, false, false
	}
	if ok && d.Session != in.Session {
		fail(w, 409, "driver ID already owned by another session")
		return nil, false, false
	}
	if len(in.Capabilities) > 24 || len(in.ControllerState) > 32 {
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
	manual := s.manualControls[id]
	if in.ControllerParserResetFresh {
		if !in.ControllerStatusFresh || (in.ControllerState != "Idle" && in.ControllerState != "Alarm") {
			fail(w, http.StatusBadRequest, "parser reset confirmation requires a fresh terminal controller status")
			return nil, false, false
		}
		if _, quarantined := s.parserQuarantine[id]; !quarantined {
			fail(w, http.StatusConflict, "driver has no pending parser reset quarantine")
			return nil, false, false
		}
		if err := s.updateParserQuarantine(id, ""); err != nil {
			fail(w, http.StatusInternalServerError, "could not persist parser reset confirmation")
			return nil, false, false
		}
		if manual != nil {
			manual.RequireParserReset = false
			manual.RequireFreshStatus = true
		}
	}
	recordSettings(&d, in)
	if in.Capabilities != nil {
		if d.Busy && !sameCapabilities(d.Capabilities, in.Capabilities) {
			fail(w, http.StatusConflict, "worker capabilities cannot change during an active lease")
			return nil, false, false
		}
		if !d.Busy {
			d.Capabilities = append([]string(nil), in.Capabilities...)
		}
	}
	recordManualControllerStatus(&d, manual, in)
	d.ID = id
	d.Session = in.Session
	d.CredentialID = cause.actor.ID
	d.LastSeen = time.Now().UTC()
	if in.ManualLease != "" {
		if err := manualReceiptError(manual, in); err != nil {
			fail(w, http.StatusConflict, err.Error())
			return nil, false, false
		}
		if in.JobID != "" || in.State != "" || in.Acked != 0 || in.Total != 0 || in.Execution != nil || in.Error != "" {
			fail(w, http.StatusBadRequest, "manual-control reports cannot include job progress or G-code execution data")
			return nil, false, false
		}
		switch in.ManualState {
		case manualStateRunning:
			if manual.State != manualStateCanceling {
				manual.State = manualStateRunning
			}
		case manualStateCompleted, manualStateFailed, manualStateCanceled:
			if in.ManualState == manualStateCanceled && manual.State != manualStateCanceling {
				fail(w, http.StatusConflict, "manual-control cancellation was not requested")
				return nil, false, false
			}
			resetWasRequired := strings.Contains(in.ManualError, "parser reset cleared the partial command") || strings.Contains(in.ManualError, "manual command parser reset was not confirmed")
			if in.ControllerState == "Idle" && (!resetWasRequired || in.ControllerParserResetFresh) && !strings.Contains(in.ManualError, "manual command parser reset was not confirmed") && s.parserQuarantine[id] != "" {
				if err := s.updateParserQuarantine(id, ""); err != nil {
					fail(w, http.StatusInternalServerError, "could not persist manual-control completion")
					return nil, false, false
				}
			}
			if strings.Contains(in.ManualError, "manual command parser reset was not confirmed") {
				if err := s.updateParserQuarantine(id, in.ManualError); err != nil {
					fail(w, http.StatusInternalServerError, "could not persist parser reset quarantine")
					return nil, false, false
				}
				manual.State = manualStateInterrupted
				manual.RequireFreshStatus = true
				manual.RequireParserReset = true
				d.ControllerState = ""
				manual.Error = in.ManualError
			} else if manualMotion(manual.Action) && in.ControllerState != "Idle" {
				if err := s.updateParserQuarantine(id, "parser reset required: manual motion ended without confirmed Idle"); err != nil {
					fail(w, http.StatusInternalServerError, "could not persist interrupted manual parser quarantine")
					return nil, false, false
				}
				manual.State = manualStateInterrupted
				manual.RequireFreshStatus = true
				d.ControllerState = ""
				manual.Error = "motion ended without a fresh Idle controller report; inspect the machine before release"
			} else {
				manual.State = in.ManualState
				manual.Error = in.ManualError
			}
		default:
			fail(w, http.StatusBadRequest, "invalid manual-control state")
			return nil, false, false
		}
		if len(manual.Error) > 1000 {
			manual.Error = manual.Error[:1000]
		}
		manual.Updated = d.LastSeen
		manual.ExpiresAt = d.LastSeen.Add(driverTTL)
		d.Busy = manualAssignmentActive(manual.State)
	} else if in.ManualState != "" || in.ManualError != "" {
		fail(w, http.StatusBadRequest, "manual state and error require a manual lease")
		return nil, false, false
	}
	if in.JobID != "" {
		j, exists := s.jobs[in.JobID]
		if !exists || j.DriverID != id || j.Lease == "" || j.Lease != in.Lease {
			fail(w, 409, "stale or invalid job lease")
			return nil, false, false
		}
		previousState := j.State
		if !activeJob(j.State) {
			// Repeated terminal receipts are idempotent after response loss.
			if j.State != in.State || !matchesTerminalReceipt(j, in) {
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
			if !activeJob(j.State) {
				j.TerminalReceiptDigest = terminalReceiptDigest(in)
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
				if err := s.save(jobs, cause); err != nil {
					fail(w, 500, "could not persist progress")
					return nil, false, false
				}
			}
		}
	}
	d.Busy = false
	if manual != nil && manualAssignmentActive(manual.State) {
		d.Busy = true
		if manual.State == manualStateQueued || manual.State == manualStateAssigned && manual.Session == in.Session {
			manual.State = manualStateAssigned
			manual.Session = in.Session
			manual.Updated = d.LastSeen
			manual.ExpiresAt = d.LastSeen.Add(driverTTL)
			s.drivers[id] = d
			return &pollAssignment{command: "manual", manual: manual}, false, true
		}
		s.drivers[id] = d
		if manual.State == manualStateCanceling {
			return &pollAssignment{command: "manual_cancel", manual: manual}, false, true
		}
		if in.ManualLease != "" && manual.State == manualStateRunning {
			return &pollAssignment{command: "manual_running", manual: manual}, false, true
		}
		// Do not replay an assignment after a worker session changes. Keep the
		// driver busy until the operator inspects and releases this lease.
		return nil, false, true
	}
	if s.parserQuarantine[id] != "" {
		d.Busy = true
		s.drivers[id] = d
		return nil, false, true
	}
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
			return &pollAssignment{command: command, job: workerJob{Job: j.Job, Lease: j.Lease, ExpectedSettings: expectedSettings(j.Job)}, record: j, chunked: hasCapability(d, jobtransfer.Capability)}, needsProgram, true
		}
	}
	s.drivers[id] = d
	return nil, false, true
}
