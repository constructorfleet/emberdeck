package driver

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Vector is a coordinate or work offset reported by a GRBL-compatible
// controller. Values use the controller's reporting units ($13), which must
// be confirmed before callers compare them with millimetre toolpaths.
type Vector struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
	A float64 `json:"a,omitempty"`
}

// StatusReport contains one parsed GRBL status response. MPos and WPos are
// mutually exclusive on standard GRBL 1.1; WCO is emitted intermittently.
type StatusReport struct {
	State           string  `json:"state"`
	MachinePosition *Vector `json:"machine_position,omitempty"`
	WorkPosition    *Vector `json:"work_position,omitempty"`
	WorkOffset      *Vector `json:"work_offset,omitempty"`
	LineNumber      *int    `json:"line_number,omitempty"`
}

// ParseStatusReport parses a GRBL 1.1 <...> status line and rejects malformed
// coordinates. Unknown fields remain forward compatible and are ignored.
func ParseStatusReport(line string) (StatusReport, error) {
	line = strings.TrimSpace(line)
	if len(line) < 3 || line[0] != '<' || line[len(line)-1] != '>' {
		return StatusReport{}, fmt.Errorf("driver: invalid status report")
	}
	fields := strings.Split(line[1:len(line)-1], "|")
	if len(fields) == 0 || fields[0] == "" {
		return StatusReport{}, fmt.Errorf("driver: status report has no state")
	}
	result := StatusReport{State: fields[0]}
	seen := make(map[string]bool, 4)
	for _, field := range fields[1:] {
		key, value, found := strings.Cut(field, ":")
		if !found || (key != "MPos" && key != "WPos" && key != "WCO" && key != "Ln") {
			continue
		}
		if seen[key] {
			return StatusReport{}, fmt.Errorf("driver: duplicate %s in status report", key)
		}
		seen[key] = true
		if key == "Ln" {
			lineNumber, err := strconv.Atoi(value)
			if err != nil || lineNumber < 0 {
				return StatusReport{}, fmt.Errorf("driver: invalid Ln: expected a nonnegative integer")
			}
			result.LineNumber = &lineNumber
			continue
		}
		vector, err := parseVector(value)
		if err != nil {
			return StatusReport{}, fmt.Errorf("driver: invalid %s: %w", key, err)
		}
		switch key {
		case "MPos":
			result.MachinePosition = &vector
		case "WPos":
			result.WorkPosition = &vector
		case "WCO":
			result.WorkOffset = &vector
		}
	}
	if result.MachinePosition != nil && result.WorkPosition != nil {
		return StatusReport{}, fmt.Errorf("driver: status report contains both MPos and WPos")
	}
	return result, nil
}

func parseVector(raw string) (Vector, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 3 && len(parts) != 4 {
		return Vector{}, fmt.Errorf("expected three or four axes")
	}
	values := make([]float64, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return Vector{}, fmt.Errorf("axis %d is not finite numeric data", i+1)
		}
		values[i] = value
	}
	vector := Vector{X: values[0], Y: values[1], Z: values[2]}
	if len(values) == 4 {
		vector.A = values[3]
	}
	return vector, nil
}

// plannerBlocksAvailable returns the Bf planner block count from a GRBL status
// line, or -1 when it is absent or malformed. Bf is diagnostic, so a malformed
// value is ignored rather than treated as a controller fault.
func plannerBlocksAvailable(line string) int {
	line = strings.TrimSpace(line)
	if len(line) < 3 || line[0] != '<' || line[len(line)-1] != '>' {
		return -1
	}
	for _, field := range strings.Split(line[1:len(line)-1], "|") {
		value, found := strings.CutPrefix(field, "Bf:")
		if !found {
			continue
		}
		blocks, _, _ := strings.Cut(value, ",")
		if n, err := strconv.Atoi(blocks); err == nil && n >= 0 {
			return n
		}
		return -1
	}
	return -1
}

// motionProgress remembers the previous fresh status report so a stream can
// tell a moving controller from one that repeats an unchanged report.
type motionProgress struct {
	known         bool
	position      Vector
	work          bool
	hasPosition   bool
	plannerBlocks int
}

// observe records a fresh status report and its Bf planner block count (-1 when
// absent). It reports whether the position or planner block count changed since
// the previous fresh report. Unparseable reports reset the comparison and never
// count as progress.
func (p *motionProgress) observe(report StatusReport, parsed bool, plannerBlocks int) bool {
	if !parsed {
		*p = motionProgress{}
		return false
	}
	next := motionProgress{known: true, plannerBlocks: plannerBlocks}
	switch {
	case report.MachinePosition != nil:
		next.position, next.hasPosition = *report.MachinePosition, true
	case report.WorkPosition != nil:
		next.position, next.hasPosition, next.work = *report.WorkPosition, true, true
	}
	moved := p.known && p.hasPosition && next.hasPosition && p.work == next.work && p.position != next.position
	planned := p.known && p.plannerBlocks >= 0 && next.plannerBlocks >= 0 && p.plannerBlocks != next.plannerBlocks
	*p = next
	return moved || planned
}
