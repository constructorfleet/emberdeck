package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

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
	jobs := s.copyJobs()
	changed := false
	expired := []string{}
	for id, d := range s.drivers {
		if time.Since(d.LastSeen) > driverTTL {
			for jid, j := range jobs {
				if j.DriverID == id && activeJob(j.State) {
					j.State = "interrupted"
					j.Error = "driver heartbeat expired; inspect machine"
					j.Lease = ""
					j.Execution = markExecutionUnknown(j.Execution)
					jobs[jid] = j
					changed = true
				}
			}
			expired = append(expired, id)
		}
	}
	if changed {
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
	s.mu.Lock()
	preflightJob, exists := s.jobs[r.PathValue("id")]
	if !exists {
		s.mu.Unlock()
		fail(w, 404, "job not found")
		return
	}
	if preflightJob.State != "queued" {
		s.mu.Unlock()
		fail(w, 409, "only queued jobs can start")
		return
	}
	if preflightJob.Provenance == nil {
		review := s.currentReview(preflightJob)
		if (review == nil || review.BoundsStatus != "in_bounds") && !in.BoundsOverride {
			s.mu.Unlock()
			fail(w, 409, "program bounds are unknown or outside the selected profile; explicit bounds_override confirmation is required")
			return
		}
	}
	s.mu.Unlock()
	if err := s.preflight(r.Context(), r.PathValue("id")); err != nil {
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
	if j.Provenance == nil {
		review := s.currentReview(j)
		if (review == nil || review.BoundsStatus != "in_bounds") && !in.BoundsOverride {
			fail(w, 409, "program bounds are unknown or outside the selected profile; explicit bounds_override confirmation is required")
			return
		}
	}
	d, ok := s.drivers[in.DriverID]
	if !ok || d.Busy {
		fail(w, 409, "driver is offline or busy")
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
	jobs := s.copyJobs()
	jobs[j.ID] = j
	if err := s.save(jobs); err != nil {
		fail(w, 500, "could not persist assignment")
		return
	}
	d.Busy = true
	s.drivers[d.ID] = d
	respond(w, 200, j)
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
	respond(w, 200, j)
}
func (s *Server) pollDriver(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !driverName.MatchString(id) {
		fail(w, 400, "invalid driver ID")
		return
	}
	var in struct {
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
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Session == "" || len(in.Session) > 128 {
		fail(w, 400, "worker session is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return
	}
	d, ok := s.drivers[id]
	if ok && d.Session != in.Session {
		fail(w, 409, "driver ID already owned by another session")
		return
	}
	if len(in.Capabilities) > 16 || len(in.ControllerState) > 32 {
		fail(w, 400, "invalid controller metadata")
		return
	}
	for _, capability := range in.Capabilities {
		if !driverName.MatchString(capability) {
			fail(w, 400, "invalid driver capability")
			return
		}
	}
	if in.Capabilities != nil {
		d.Capabilities = append([]string(nil), in.Capabilities...)
	}
	if in.ControllerState != "" {
		d.ControllerState = in.ControllerState
	}
	d.ID = id
	d.Session = in.Session
	d.LastSeen = time.Now().UTC()
	if in.JobID != "" {
		j, exists := s.jobs[in.JobID]
		if !exists || j.DriverID != id || j.Lease == "" || j.Lease != in.Lease {
			fail(w, 409, "stale or invalid job lease")
			return
		}
		if !activeJob(j.State) {
			// Repeated terminal receipts are idempotent after response loss.
			if j.State != in.State {
				fail(w, 409, "job is already terminal")
				return
			}
		} else {
			if in.Acked < j.Acked || in.Total < 0 || in.Acked > in.Total || in.Acked < 0 || (j.Total != 0 && j.Total != in.Total) {
				fail(w, 400, "invalid progress")
				return
			}
			if in.Execution != nil && !validExecution(*in.Execution, j.Gcode, j.Execution, in.State) {
				fail(w, 400, "invalid execution telemetry")
				return
			}
			switch in.State {
			case "pausing", "resuming":
				if !hasCapability(d, "pause_resume") {
					fail(w, 400, "worker does not support pause and resume")
					return
				}
			case "running":
				if j.State == "resuming" && (in.ControllerState == "Run" || in.ControllerState == "Idle") {
					j.State = "running"
				}
			case "paused":
				if !hasCapability(d, "pause_resume") || (in.ControllerState != "Hold:0" && in.ControllerState != "Idle") {
					fail(w, 400, "paused requires confirmed controller hold or Idle")
					return
				}
				if j.State != "canceling" && j.State != "resuming" {
					j.State = "paused"
				}
			case "completed":
				if in.Total == 0 || in.Acked != in.Total || (hasCapability(d, "pause_resume") && in.ControllerState != "Idle") {
					fail(w, 409, "completion requires all lines acknowledged and controller Idle")
					return
				}
				j.State = "completed"
			case "failed":
				j.State = "failed"
			case "canceled":
				if j.State != "canceling" {
					fail(w, 409, "cancellation was not requested")
					return
				}
				j.State = "canceled"
			default:
				fail(w, 400, "invalid worker state")
				return
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
			jobs := s.copyJobs()
			jobs[j.ID] = j
			if err := s.save(jobs); err != nil {
				fail(w, 500, "could not persist progress")
				return
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
			respond(w, 200, map[string]any{"command": command, "job": j, "lease": j.Lease})
			return
		}
	}
	s.drivers[id] = d
	respond(w, 200, map[string]string{"command": "idle", "protocol": "1"})
}
