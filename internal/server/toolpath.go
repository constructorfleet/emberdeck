package server

import (
	"math"
	"net/http"
	"time"

	"github.com/ConstructorFleet/emberdeck/toolpath"
)

func (s *Server) getToolpath(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	job, ok := s.jobs[r.PathValue("id")]
	s.mu.Unlock()
	if !ok {
		fail(w, http.StatusNotFound, "job not found")
		return
	}
	respond(w, http.StatusOK, toolpath.Parse(job.Gcode))
}

func validExecution(execution toolpath.Execution, gcode string, previous *toolpath.Execution, jobState string) bool {
	progress := execution.Progress
	total := len(toolpath.Parse(gcode).Segments)
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
