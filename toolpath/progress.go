package toolpath

import (
	"math"
	"time"
)

const positionToleranceMM = 0.75

// Progress is a conservative estimate correlated from an observed work
// position. Completed is a monotonic lower bound, Active is a zero-based
// segment index when the position identifies one segment, and Confidence is
// one of estimated, ambiguous, or unknown.
type Progress struct {
	Completed  int    `json:"completed"`
	Total      int    `json:"total"`
	Active     *int   `json:"active,omitempty"`
	Confidence string `json:"confidence"`
}

// Position is a controller-reported vector. It is kept separate from Point so
// the raw coordinate frame and units remain explicit at the HTTP seam.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z,omitempty"`
	A float64 `json:"a,omitempty"`
}

// Execution is the latest controller observation and its correlated path
// estimate. PositionUnits is "assumed-mm" only for the current estimated
// GRBL correlation; a caller must expose that assumption to the operator.
type Execution struct {
	MachinePosition    *Position `json:"machine_position,omitempty"`
	WorkPosition       *Position `json:"work_position,omitempty"`
	WorkOffset         *Position `json:"work_offset,omitempty"`
	LineNumber         *int      `json:"line_number,omitempty"`
	PositionUnits      string    `json:"position_units,omitempty"`
	PositionReportedAt time.Time `json:"position_reported_at,omitempty"`
	Progress           Progress  `json:"path_progress"`
}

// Tracker correlates controller positions with an ordered parsed path. It does
// not consume or inspect command acknowledgements. Overlapping/repeated path
// locations are reported as ambiguous; no output proves laser energy or
// material processing.
type Tracker struct {
	segments  []Segment
	supported bool
	completed int
	lastIndex int
}

// NewTracker creates a position tracker. Programs containing unsupported
// motion retain geometry for preview, but cannot produce execution progress.
func NewTracker(program Program) *Tracker {
	return &Tracker{segments: append([]Segment(nil), program.Segments...), supported: program.Supported}
}

// Update observes one work-coordinate position and returns the latest
// confidence-labeled progress estimate.
func (t *Tracker) Update(point Point) Progress {
	if t == nil {
		return Progress{Confidence: "unknown"}
	}
	progress := Progress{Completed: t.completed, Total: len(t.segments), Confidence: "unknown"}
	if !t.supported || len(t.segments) == 0 || !finite(point.X) || !finite(point.Y) {
		return progress
	}
	type candidate struct {
		index int
		along float64
	}
	candidates := make([]candidate, 0, 2)
	for index, segment := range t.segments {
		dx, dy := segment.End.X-segment.Start.X, segment.End.Y-segment.Start.Y
		lengthSquared := dx*dx + dy*dy
		if lengthSquared == 0 {
			continue
		}
		along := ((point.X-segment.Start.X)*dx + (point.Y-segment.Start.Y)*dy) / lengthSquared
		if along < 0 {
			along = 0
		} else if along > 1 {
			along = 1
		}
		closestX, closestY := segment.Start.X+along*dx, segment.Start.Y+along*dy
		distance := math.Hypot(point.X-closestX, point.Y-closestY)
		if distance <= positionToleranceMM {
			candidates = append(candidates, candidate{index: index, along: along})
		}
	}
	if len(candidates) == 0 {
		return progress
	}
	if len(candidates) == 1 && candidates[0].index < t.lastIndex {
		progress.Confidence = "ambiguous"
		return progress
	}
	minimumReached := len(t.segments)
	for _, candidate := range candidates {
		reached := candidate.index
		if candidate.along >= 0.98 {
			reached++
		}
		if reached < minimumReached {
			minimumReached = reached
		}
	}
	if minimumReached > t.completed {
		t.completed = minimumReached
	}
	progress.Completed = t.completed
	if len(candidates) == 1 {
		t.lastIndex = candidates[0].index
		progress.Confidence = "estimated"
		if candidates[0].along < 0.98 {
			active := candidates[0].index
			progress.Active = &active
		}
		return progress
	}
	progress.Confidence = "ambiguous"
	return progress
}

// UpdateLineNumber uses GRBL's optional Ln execution marker. It is available
// only when the controller was compiled to report line numbers and input lines
// carry matching N words. It returns unknown when the marker cannot map to one
// visualized segment.
func (t *Tracker) UpdateLineNumber(lineNumber int) Progress {
	if t == nil {
		return Progress{Confidence: "unknown"}
	}
	progress := Progress{Completed: t.completed, Total: len(t.segments), Confidence: "unknown"}
	if !t.supported || lineNumber < 0 {
		return progress
	}
	index := -1
	for i, segment := range t.segments {
		if segment.HasLineNumber && segment.LineNumber == lineNumber {
			if index >= 0 {
				progress.Confidence = "ambiguous"
				return progress
			}
			index = i
		}
	}
	if index < 0 {
		return progress
	}
	if index < t.lastIndex {
		progress.Confidence = "ambiguous"
		return progress
	}
	t.lastIndex = index
	if index > t.completed {
		t.completed = index
	}
	active := index
	progress.Completed, progress.Active, progress.Confidence = t.completed, &active, "reported"
	return progress
}

// Finish may be called only after the driver observed a fresh controller Idle
// after the program's final acknowledgement and beam-off cleanup.
func (t *Tracker) Finish() Progress {
	if t == nil || !t.supported || len(t.segments) == 0 {
		if t == nil {
			return Progress{Confidence: "unknown"}
		}
		return Progress{Completed: t.completed, Total: len(t.segments), Confidence: "unknown"}
	}
	t.completed = len(t.segments)
	return Progress{Completed: t.completed, Total: len(t.segments), Confidence: "controller_idle"}
}

// Unknown preserves the monotonic completed lower bound while clearing any
// active-segment claim when fresh execution evidence is unavailable.
func (t *Tracker) Unknown() Progress {
	if t == nil {
		return Progress{Confidence: "unknown"}
	}
	return Progress{Completed: t.completed, Total: len(t.segments), Confidence: "unknown"}
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
