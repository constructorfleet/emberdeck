package server

import (
	"math"
	"net/http"
	"time"

	"github.com/ConstructorFleet/emberdeck/toolpath"
)

func (s *Server) getToolpath(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	record, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "job not found")
		return
	}
	gcode, ok := s.program(w, record)
	if !ok {
		return
	}
	respond(w, http.StatusOK, toolpath.Parse(gcode))
}

// ProgramReview compares an uploaded program's known path bounds with the
// machine profile selected at upload. It is stored with job metadata.
type ProgramReview struct {
	ProfileID       string           `json:"profile_id,omitempty"`
	ProfileRevision int              `json:"profile_revision,omitempty"`
	ProfileStale    bool             `json:"profile_stale,omitempty"`
	BoundsStatus    string           `json:"bounds_status"`
	Bounds          *toolpath.Bounds `json:"bounds,omitempty"`
	ProfileBounds   *toolpath.Bounds `json:"profile_bounds,omitempty"`
	Warnings        []string         `json:"warnings"`
	// ConstantPowerM3 reports an M3 (constant laser power) command.
	ConstantPowerM3 *bool `json:"constant_power_m3,omitempty"`
}

func reviewProgram(source string, profile *MachineProfile, stale bool) ProgramReview {
	program := toolpath.Parse(source)
	review := ProgramReview{BoundsStatus: "unknown", Warnings: append([]string{}, program.Warnings...), ConstantPowerM3: constantPowerFlag(source)}
	if bounds, ok := toolpath.ProgramBounds(program); ok {
		review.Bounds = &bounds
	}
	if profile == nil {
		review.Warnings = append(review.Warnings, "no current machine profile is selected")
		return review
	}
	review.ProfileID, review.ProfileRevision = profile.ID, profile.Revision
	review.ProfileBounds = &toolpath.Bounds{Min: toolpath.Point{}, Max: toolpath.Point{X: profile.WidthMM, Y: profile.HeightMM}}
	if stale {
		review.ProfileStale = true
		review.Warnings = append(review.Warnings, "the declared machine profile changed or was deleted after this job was created")
		return review
	}
	if review.Bounds == nil {
		review.Warnings = append(review.Warnings, "no complete 2D path bounds could be determined")
		return review
	}
	if !program.Supported {
		review.Warnings = append(review.Warnings, "unsupported motion prevents a reliable profile comparison")
		return review
	}
	if review.Bounds.Min.X < 0 || review.Bounds.Min.Y < 0 || review.Bounds.Max.X > profile.WidthMM || review.Bounds.Max.Y > profile.HeightMM {
		review.BoundsStatus = "out_of_bounds"
		return review
	}
	if program.Supported && program.Complete {
		review.BoundsStatus = "in_bounds"
	} else {
		review.Warnings = append(review.Warnings, "program contains unsupported or incomplete motion; full bounds are unknown")
	}
	return review
}

// currentReview returns the stored review, marked unknown if the selected
// profile changed since upload. Callers hold the server mutex.
func (s *Server) currentReview(job Job) *ProgramReview {
	if job.Provenance != nil {
		return nil
	}
	copy := ProgramReview{BoundsStatus: "unknown", Warnings: []string{"program review is unavailable"}}
	if job.Review != nil {
		copy = *job.Review
		copy.Warnings = append([]string(nil), job.Review.Warnings...)
	}
	if job.ProfileID == "" {
		return &copy
	}
	profile, exists := s.profiles[job.ProfileID]
	if !exists || profile.Revision != job.ProfileRevision {
		copy.BoundsStatus = "unknown"
		copy.ProfileStale = true
		copy.Warnings = append(copy.Warnings, "the declared machine profile changed or was deleted after this job was created")
	}
	return &copy
}

func (s *Server) getJobReview(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	record, exists := s.jobs[r.PathValue("id")]
	if !exists {
		s.mu.Unlock()
		fail(w, http.StatusNotFound, "job not found")
		return
	}
	review := s.currentReview(record.Job)
	var generatedStale bool
	if record.Provenance != nil {
		profile, exists := s.profiles[record.Provenance.Profile.ID]
		generatedStale = !exists || profile.Revision != record.Provenance.Profile.Revision
	}
	s.mu.Unlock()
	gcode, ok := s.program(w, record)
	if !ok {
		return
	}
	if review == nil && record.Provenance != nil {
		profile := record.Provenance.Profile
		generatedReview := reviewProgram(gcode, &profile, generatedStale)
		review = &generatedReview
	}
	program := toolpath.Parse(gcode)
	respond(w, http.StatusOK, struct {
		*ProgramReview
		Program toolpath.Program `json:"program"`
	}{review, program})
}

func validExecution(execution toolpath.Execution, segments int, previous *toolpath.Execution, jobState string) bool {
	progress := execution.Progress
	total := segments
	if previous != nil {
		total = previous.Progress.Total
	}
	if progress.Total != total || progress.Completed < 0 || progress.Completed > progress.Total {
		return false
	}
	if progress.Active != nil && (*progress.Active < 0 || *progress.Active >= progress.Total) {
		return false
	}
	switch progress.Confidence {
	case "estimated", "reported", "ambiguous", "unknown":
	case "controller_idle":
		if jobState != "completed" || progress.Completed != progress.Total || progress.Total == 0 {
			return false
		}
	default:
		return false
	}
	if progress.Confidence == "reported" && execution.LineNumber == nil {
		return false
	}
	if progress.Confidence == "estimated" && (execution.WorkPosition == nil || execution.PositionUnits != "assumed-mm") {
		return false
	}
	if execution.PositionUnits != "" && execution.PositionUnits != "controller" && execution.PositionUnits != "assumed-mm" {
		return false
	}
	for _, position := range []*toolpath.Position{execution.MachinePosition, execution.WorkPosition, execution.WorkOffset} {
		if position != nil && (!finiteCoordinate(position.X) || !finiteCoordinate(position.Y) || !finiteCoordinate(position.Z) || !finiteCoordinate(position.A)) {
			return false
		}
	}
	hasPosition := execution.MachinePosition != nil || execution.WorkPosition != nil
	if hasPosition {
		if execution.PositionReportedAt.IsZero() || execution.PositionReportedAt.After(time.Now().Add(5*time.Second)) {
			return false
		}
	}
	if previous != nil {
		if progress.Completed < previous.Progress.Completed {
			return false
		}
		if !execution.PositionReportedAt.IsZero() && execution.PositionReportedAt.Before(previous.PositionReportedAt) {
			return false
		}
	}
	return true
}

func finiteCoordinate(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= 10_000_000
}

func markExecutionUnknown(execution *toolpath.Execution) *toolpath.Execution {
	if execution == nil {
		return nil
	}
	copy := *execution
	copy.Progress.Active = nil
	copy.Progress.Confidence = "unknown"
	return &copy
}
