package server

import "net/http"

func activeJob(state string) bool {
	switch state {
	case "running", "pausing", "paused", "resuming", "canceling":
		return true
	default:
		return false
	}
}
func hasCapability(d Driver, capability string) bool {
	for _, value := range d.Capabilities {
		if value == capability {
			return true
		}
	}
	return false
}
func (s *Server) pauseJob(w http.ResponseWriter, r *http.Request) {
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	s.controlJob(w, r, false)
}
func (s *Server) resumeJob(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirmed bool `json:"confirmed"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !input.Confirmed {
		fail(w, 400, "operator must confirm supervision and machine readiness before resuming")
		return
	}
	s.controlJob(w, r, true)
}
func (s *Server) controlJob(w http.ResponseWriter, r *http.Request, resume bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return
	}
	job, ok := s.jobs[r.PathValue("id")]
	if !ok {
		fail(w, 404, "job not found")
		return
	}
	d, online := s.drivers[job.DriverID]
	if !online || !hasCapability(d, "pause_resume") {
		fail(w, 409, "driver does not support confirmed pause and resume")
		return
	}
	if resume {
		if job.State != "paused" {
			fail(w, 409, "only confirmed paused jobs can resume")
			return
		}
		job.State = "resuming"
	} else {
		switch job.State {
		case "running", "resuming":
			job.State = "pausing"
		case "pausing", "paused": // Repeated pause requests preserve a confirmed hold.
		default:
			fail(w, 409, "only running jobs can pause")
			return
		}
	}
	action := "pause"
	if resume {
		action = "resume"
	}
	jobs := s.copyJobs()
	jobs[job.ID] = job
	if err := s.save(jobs, operatorCause(r, action)); err != nil {
		fail(w, 500, "could not persist controller request")
		return
	}
	respond(w, 200, s.jobs[job.ID].Job)
}
