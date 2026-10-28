package server

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"unicode"
)

const monolineFontID = "emberdeck-stick-v1"
const monolineTextMaxRunes = 32
const monolineTextMinSizeMM = 1
const monolineTextMaxSizeMM = 100

type VectorText struct {
	Content string  `json:"content"`
	Font    string  `json:"font"`
	SizeMM  float64 `json:"size_mm"`
	XMM     float64 `json:"x_mm"`
	YMM     float64 `json:"y_mm"`
}

type fontPoint struct{ x, y float64 }
type fontStroke []fontPoint

func stroke(coords ...float64) fontStroke {
	points := make(fontStroke, 0, len(coords)/2)
	for i := 0; i+1 < len(coords); i += 2 {
		points = append(points, fontPoint{x: coords[i], y: coords[i+1]})
	}
	return points
}

// The font is an original, hand-authored single-line alphabet. Coordinates
// occupy a 5-by-7 cell. Lowercase source letters use uppercase stroke forms.
var monolineGlyphs = map[rune][]fontStroke{
	'A': {stroke(0, 0, 2.5, 7), stroke(2.5, 7, 5, 0), stroke(1, 3, 4, 3)},
	'B': {stroke(0, 0, 0, 7), stroke(0, 7, 3.5, 7, 5, 5.8, 5, 4.5, 3.5, 3.5, 0, 3.5), stroke(0, 3.5, 3.5, 3.5, 5, 2.2, 5, 1, 3.5, 0, 0, 0)},
	'C': {stroke(5, 6, 4, 7, 1, 7, 0, 6, 0, 1, 1, 0, 4, 0, 5, 1)},
	'D': {stroke(0, 0, 0, 7, 3.5, 7, 5, 5.5, 5, 1.5, 3.5, 0, 0, 0)},
	'E': {stroke(5, 7, 0, 7, 0, 0, 5, 0), stroke(0, 3.5, 4, 3.5)},
	'F': {stroke(0, 0, 0, 7, 5, 7), stroke(0, 3.5, 4, 3.5)},
	'G': {stroke(5, 6, 4, 7, 1, 7, 0, 6, 0, 1, 1, 0, 5, 0, 5, 3.5, 3, 3.5)},
	'H': {stroke(0, 0, 0, 7), stroke(5, 0, 5, 7), stroke(0, 3.5, 5, 3.5)},
	'I': {stroke(0, 7, 5, 7), stroke(2.5, 7, 2.5, 0), stroke(0, 0, 5, 0)},
	'J': {stroke(0, 7, 5, 7, 5, 1, 4, 0, 1, 0, 0, 1)},
	'K': {stroke(0, 0, 0, 7), stroke(5, 7, 0, 3.5, 5, 0)},
	'L': {stroke(0, 7, 0, 0, 5, 0)},
	'M': {stroke(0, 0, 0, 7, 2.5, 4, 5, 7, 5, 0)},
	'N': {stroke(0, 0, 0, 7, 5, 0, 5, 7)},
	'O': {stroke(1, 0, 0, 1, 0, 6, 1, 7, 4, 7, 5, 6, 5, 1, 4, 0, 1, 0)},
	'P': {stroke(0, 0, 0, 7, 3.5, 7, 5, 5.8, 5, 4.5, 3.5, 3.5, 0, 3.5)},
	'Q': {stroke(1, 0, 0, 1, 0, 6, 1, 7, 4, 7, 5, 6, 5, 1, 4, 0, 1, 0), stroke(3, 2, 5, 0)},
	'R': {stroke(0, 0, 0, 7, 3.5, 7, 5, 5.8, 5, 4.5, 3.5, 3.5, 0, 3.5), stroke(2.5, 3.5, 5, 0)},
	'S': {stroke(5, 6, 4, 7, 1, 7, 0, 6, 0, 4.5, 1, 3.5, 4, 3.5, 5, 2.5, 5, 1, 4, 0, 1, 0, 0, 1)},
	'T': {stroke(0, 7, 5, 7), stroke(2.5, 7, 2.5, 0)},
	'U': {stroke(0, 7, 0, 1, 1, 0, 4, 0, 5, 1, 5, 7)},
	'V': {stroke(0, 7, 2.5, 0, 5, 7)},
	'W': {stroke(0, 7, 1, 0, 2.5, 3, 4, 0, 5, 7)},
	'X': {stroke(0, 7, 5, 0), stroke(0, 0, 5, 7)},
	'Y': {stroke(0, 7, 2.5, 3.5, 5, 7), stroke(2.5, 3.5, 2.5, 0)},
	'Z': {stroke(0, 7, 5, 7, 0, 0, 5, 0)},
	'0': {stroke(1, 0, 0, 1, 0, 6, 1, 7, 4, 7, 5, 6, 5, 1, 4, 0, 1, 0)},
	'1': {stroke(0, 5, 2.5, 7, 2.5, 0, 0, 0, 5, 0)},
	'2': {stroke(0, 6, 1, 7, 4, 7, 5, 6, 5, 4.5, 0, 0, 5, 0)},
	'3': {stroke(0, 7, 4, 7, 5, 6, 5, 1, 4, 0, 0, 0), stroke(1, 3.5, 4, 3.5)},
	'4': {stroke(4, 0, 4, 7, 0, 2, 5, 2)},
	'5': {stroke(5, 7, 0, 7, 0, 3.5, 4, 3.5, 5, 2.5, 5, 1, 4, 0, 1, 0, 0, 1)},
	'6': {stroke(5, 7, 1, 7, 0, 6, 0, 1, 1, 0, 4, 0, 5, 1, 5, 2.5, 4, 3.5, 0, 3.5)},
	'7': {stroke(0, 7, 5, 7, 2, 0)},
	'8': {stroke(1, 3.5, 0, 4.5, 0, 6, 1, 7, 4, 7, 5, 6, 5, 4.5, 4, 3.5, 1, 3.5, 0, 2.5, 0, 1, 1, 0, 4, 0, 5, 1, 5, 2.5, 4, 3.5)},
	'9': {stroke(5, 3.5, 1, 3.5, 0, 4.5, 0, 6, 1, 7, 4, 7, 5, 6, 5, 1, 4, 0, 0, 0)},
	'.': {stroke(2.5, 0, 2.5, 0.1)},
	',': {stroke(2.5, 1, 2, 0)},
	':': {stroke(2.5, 5, 2.5, 5.1), stroke(2.5, 1, 2.5, 1.1)},
	';': {stroke(2.5, 5, 2.5, 5.1), stroke(2.5, 1, 2, 0)},
	'!': {stroke(2.5, 7, 2.5, 2), stroke(2.5, 0.1, 2.5, 0.2)},
	'?': {stroke(0, 6, 1, 7, 4, 7, 5, 6, 5, 5, 2.5, 3.5, 2.5, 2), stroke(2.5, 0.1, 2.5, 0.2)},
	'+': {stroke(0, 3.5, 5, 3.5), stroke(2.5, 6, 2.5, 1)},
	'-': {stroke(0.5, 3.5, 4.5, 3.5)},
	'/': {stroke(0, 0, 5, 7)},
	'_': {stroke(0, 0, 5, 0)},
	'(': {stroke(3.5, 7, 1.5, 5, 1.5, 2, 3.5, 0)},
	')': {stroke(1.5, 7, 3.5, 5, 3.5, 2, 1.5, 0)},
}

func generateMonolineText(source VectorText) ([]VectorSubpath, error) {
	if source.Font != monolineFontID {
		return nil, errors.New("unsupported text font")
	}
	if len([]rune(source.Content)) == 0 || len([]rune(source.Content)) > monolineTextMaxRunes {
		return nil, errors.New("text must contain 1 to 32 characters")
	}
	if !finite(source.SizeMM) || !validPrecision(source.SizeMM) || source.SizeMM < monolineTextMinSizeMM || source.SizeMM > monolineTextMaxSizeMM {
		return nil, errors.New("text height must be between 1 and 100 mm")
	}
	if !finite(source.XMM) || !finite(source.YMM) || !validPrecision(source.XMM) || !validPrecision(source.YMM) || source.XMM < 0 || source.YMM < 0 || source.XMM > 10000 || source.YMM > 10000 {
		return nil, errors.New("text origin must be within the 0 to 10000 mm coordinate range")
	}
	if strings.TrimSpace(source.Content) == "" {
		return nil, errors.New("text must include at least one visible character")
	}
	unit := source.SizeMM / 7
	paths := make([]VectorSubpath, 0, len(source.Content)*3)
	for index, character := range source.Content {
		if character > 0x7f {
			return nil, errors.New("text supports ASCII letters, digits, space, and .,:;!?+-/_() only")
		}
		upper := unicode.ToUpper(character)
		if character == ' ' {
			continue
		}
		strokes, ok := monolineGlyphs[upper]
		if !ok {
			return nil, errors.New("text supports ASCII letters, digits, space, and .,:;!?+-/_() only")
		}
		xOffset := float64(index) * 6 * unit
		for _, glyphStroke := range strokes {
			points := make([]VectorPoint, len(glyphStroke))
			for pointIndex, p := range glyphStroke {
				points[pointIndex] = VectorPoint{
					XMM: roundTextMM(source.XMM + xOffset + p.x*unit),
					YMM: roundTextMM(source.YMM + p.y*unit),
				}
			}
			paths = append(paths, VectorSubpath{Points: points, Closed: false})
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("text must include at least one visible character")
	}
	lastCellRight := source.XMM + float64(len([]rune(source.Content))*6-1)*unit
	if lastCellRight > 10000 || source.YMM+source.SizeMM > 10000 {
		return nil, errors.New("generated text exceeds the 10000 mm coordinate limit")
	}
	return paths, nil
}

func roundTextMM(value float64) float64 {
	return math.Round(value*1000) / 1000
}

func (s *Server) designerTextGeometry(w http.ResponseWriter, r *http.Request) {
	var input VectorText
	if !decodeBounded(w, r, &input, 4<<10) {
		return
	}
	paths, err := generateMonolineText(input)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	respond(w, http.StatusOK, map[string]any{"subpaths": paths})
}
