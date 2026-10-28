// Package toolpath parses the conservative two-dimensional linear subset used
// to visualize submitted G-code. It does not validate a machine program and
// its output must never be used to dispatch one.
package toolpath

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	MaxProgramBytes = 4 << 20
	maxSegments     = 10000
)

// Point is an XY coordinate in millimetres in the active G-code work frame.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Segment describes one known, linear XY move. SourceLine is one-based.
type Segment struct {
	SourceLine    int     `json:"source_line"`
	LineNumber    int     `json:"line_number,omitempty"`
	HasLineNumber bool    `json:"has_line_number,omitempty"`
	Start         Point   `json:"start"`
	End           Point   `json:"end"`
	FeedMMMin     float64 `json:"feed_mm_min,omitempty"`
	Rapid         bool    `json:"rapid"`
	LaserOn       bool    `json:"laser_on"`
}

// Program contains the supported part of a G-code visualization. Supported is
// false if an unmodeled motion or coordinate command occurred. Complete is
// false if any move was omitted, geometry was truncated, or a warning exists.
// Callers must show warnings and must not present this representation as a
// verified execution trace.
type Program struct {
	Segments  []Segment `json:"segments"`
	Warnings  []string  `json:"warnings"`
	Supported bool      `json:"supported"`
	Complete  bool      `json:"complete"`
}

type position struct {
	point Point
	known bool
}

// Parse reads the common G0/G1, XY, G20/G21, G90/G91, and M3/M4/M5 subset.
// Arcs, machine-coordinate moves, unknown axes, and unrecognized motion modes
// are reported as unsupported rather than approximated. The first move is
// omitted when its start position cannot be established from the program.
func Parse(source string) Program {
	result := Program{Segments: []Segment{}, Warnings: []string{}, Supported: true, Complete: true}
	if len(source) > MaxProgramBytes {
		result.Supported, result.Complete = false, false
		result.Warnings = append(result.Warnings, "program exceeds the 4 MiB visualization limit")
		return result
	}
	unitScale, absolute, motion := 1.0, true, 0
	feed, power := 0.0, 0.0
	laserMode := 0
	current := position{}
	warn := func(message string, supported bool) {
		result.Complete = false
		if !supported {
			result.Supported = false
		}
		for _, existing := range result.Warnings {
			if existing == message {
				return
			}
		}
		if len(result.Warnings) < 32 {
			result.Warnings = append(result.Warnings, message)
		}
	}

	for lineIndex, line := range strings.Split(source, "\n") {
		clean := stripComments(line)
		if clean == "" || clean == "%" {
			continue
		}
		words, ok := parseWords(clean)
		if !ok {
			warn(fmt.Sprintf("line %d contains an unreadable word", lineIndex+1), false)
			current.known = false
			continue
		}
		blockMotion, hasMotion := -1, false
		machineMove := false
		blockModes := []float64{}
		axes := map[byte]float64{}
		hasAxis := false
		blockFeed, hasFeed := 0.0, false
		unsupportedBlock := false
		lineNumber, hasLineNumber := 0, false
		for _, word := range words {
			switch word.letter {
			case 'G':
				blockModes = append(blockModes, word.value)
				switch {
				case code(word.value, 0):
					blockMotion, hasMotion = 0, true
				case code(word.value, 1):
					blockMotion, hasMotion = 1, true
				case code(word.value, 20):
					unitScale = 25.4
				case code(word.value, 21):
					unitScale = 1
				case code(word.value, 90):
					absolute = true
				case code(word.value, 91):
					absolute = false
				case code(word.value, 17), code(word.value, 40), code(word.value, 49), code(word.value, 80), code(word.value, 94):
					// These modes do not change the supported XY linear path model.
				case code(word.value, 54), code(word.value, 55), code(word.value, 56), code(word.value, 57), code(word.value, 58), code(word.value, 59):
					warn(fmt.Sprintf("line %d changes work coordinate system; offsets are not modeled", lineIndex+1), false)
					unsupportedBlock = true
					current.known = false
				case code(word.value, 53):
					machineMove = true
				case code(word.value, 2), code(word.value, 3):
					blockMotion, hasMotion = -2, true
				default:
					warn(fmt.Sprintf("line %d uses unsupported G-code G%s", lineIndex+1, formatCode(word.value)), false)
					unsupportedBlock = true
					current.known = false
				}
			case 'M':
				switch {
				case code(word.value, 3), code(word.value, 4):
					laserMode = int(math.Round(word.value))
				case code(word.value, 5), code(word.value, 2), code(word.value, 30), code(word.value, 8), code(word.value, 9):
					if code(word.value, 5) || code(word.value, 2) || code(word.value, 30) {
						laserMode = 0
					}
				default:
					warn(fmt.Sprintf("line %d uses unmodeled M-code M%s", lineIndex+1, formatCode(word.value)), false)
					unsupportedBlock = true
					current.known = false
				}
			case 'X', 'Y':
				axes[word.letter] = word.value
				hasAxis = true
			case 'Z':
				// Z-only focus moves do not contribute to the XY view.
			case 'F':
				blockFeed, hasFeed = word.value, true
			case 'S':
				power = word.value
			case 'N':
				if word.value < 0 || word.value != math.Trunc(word.value) || word.value > 2147483647 {
					warn(fmt.Sprintf("line %d has an invalid N line number", lineIndex+1), false)
					unsupportedBlock = true
					continue
				}
				lineNumber, hasLineNumber = int(word.value), true
			case 'T', 'P', 'L':
				// Line labels and common non-geometric parameters.
			default:
				warn(fmt.Sprintf("line %d uses unsupported axis or word %c", lineIndex+1, word.letter), false)
				if strings.ContainsRune("ABCUVW", rune(word.letter)) {
					current.known = false
				}
			}
		}
		for axis, value := range axes {
			axes[axis] = value * unitScale
		}
		if hasFeed {
			feed = blockFeed * unitScale
		}
		if unsupportedBlock && hasAxis {
			continue
		}
		if !hasMotion && hasAxis {
			if motion == 0 || motion == 1 {
				blockMotion = motion
				hasMotion = true
			} else {
				warn(fmt.Sprintf("line %d has coordinates without a supported motion mode", lineIndex+1), false)
				current.known = false
			}
		}
		for _, mode := range blockModes {
			if code(mode, 2) || code(mode, 3) {
				warn(fmt.Sprintf("line %d contains an unsupported arc", lineIndex+1), false)
				if hasAxis && absolute && len(axes) >= 2 {
					current = position{point: Point{X: axes['X'], Y: axes['Y']}, known: true}
				} else {
					current.known = false
				}
				blockMotion = -2
			}
		}
		if hasMotion && blockMotion >= 0 {
			motion = blockMotion
		}
		if !hasMotion || !hasAxis || blockMotion == -2 {
			continue
		}
		if machineMove {
			warn(fmt.Sprintf("line %d uses unsupported machine coordinates", lineIndex+1), false)
			current.known = false
			continue
		}

		start := current
		next := current
		if absolute {
			if value, exists := axes['X']; exists {
				next.point.X = value
			} else if !current.known {
				next.known = false
			}
			if value, exists := axes['Y']; exists {
				next.point.Y = value
			} else if !current.known {
				next.known = false
			}
			if len(axes) >= 2 || current.known {
				next.known = true
			}
		} else if current.known {
			if value, exists := axes['X']; exists {
				next.point.X += value
			}
			if value, exists := axes['Y']; exists {
				next.point.Y += value
			}
		} else {
			next.known = false
		}

		if start.known && next.known && start.point != next.point {
			if len(result.Segments) == maxSegments {
				warn("toolpath exceeds the 10000-segment visualization limit", false)
				break
			}
			result.Segments = append(result.Segments, Segment{
				SourceLine:    lineIndex + 1,
				LineNumber:    lineNumber,
				HasLineNumber: hasLineNumber,
				Start:         start.point,
				End:           next.point,
				FeedMMMin:     feed,
				Rapid:         blockMotion == 0,
				LaserOn:       blockMotion == 1 && laserMode != 0 && power > 0,
			})
		} else if !start.known && next.known {
			warn(fmt.Sprintf("line %d starts from an unknown position; its initial move is omitted", lineIndex+1), true)
		}
		current = next
	}
	return result
}

type word struct {
	letter byte
	value  float64
}

func parseWords(line string) ([]word, bool) {
	words := make([]word, 0, 8)
	for i := 0; i < len(line); {
		if line[i] == ' ' || line[i] == '\t' || line[i] == '\r' || line[i] == '/' {
			i++
			continue
		}
		letter := line[i]
		if letter >= 'a' && letter <= 'z' {
			letter -= 'a' - 'A'
		}
		if letter < 'A' || letter > 'Z' {
			return nil, false
		}
		i++
		start := i
		if i < len(line) && (line[i] == '+' || line[i] == '-') {
			i++
		}
		digits := 0
		for i < len(line) && line[i] >= '0' && line[i] <= '9' {
			i++
			digits++
		}
		if i < len(line) && line[i] == '.' {
			i++
			for i < len(line) && line[i] >= '0' && line[i] <= '9' {
				i++
				digits++
			}
		}
		if digits == 0 {
			return nil, false
		}
		value, err := strconv.ParseFloat(line[start:i], 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, false
		}
		words = append(words, word{letter: letter, value: value})
	}
	return words, len(words) > 0
}

func stripComments(line string) string {
	if index := strings.IndexByte(line, ';'); index >= 0 {
		line = line[:index]
	}
	var out strings.Builder
	depth := 0
	for _, r := range line {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				out.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(out.String())
}

func code(value, expected float64) bool { return math.Abs(value-expected) < 0.000001 }

func formatCode(value float64) string { return strconv.FormatFloat(value, 'f', -1, 64) }
