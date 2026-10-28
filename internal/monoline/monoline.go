// Package monoline provides the deterministic EmberDeck Stick single-line glyphs.
package monoline

import (
	"errors"
	"strings"
	"unicode"
)

const FontID = "emberdeck-stick-v1"
const MaxRunes = 32

type Point struct{ X, Y float64 }
type Stroke []Point

func stroke(coords ...float64) Stroke {
	points := make(Stroke, 0, len(coords)/2)
	for i := 0; i+1 < len(coords); i += 2 {
		points = append(points, Point{X: coords[i], Y: coords[i+1]})
	}
	return points
}

// glyphs are hand-authored strokes in a 5-by-7 cell; character advance is six.
var glyphs = map[rune][]Stroke{
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

// Outline returns open strokes in a 5-by-7, y-up coordinate system with a
// six-unit advance for each source character. The caller controls scale/origin.
func Outline(content string) ([]Stroke, error) {
	runes := []rune(content)
	if len(runes) == 0 || len(runes) > MaxRunes || strings.TrimSpace(content) == "" {
		return nil, errors.New("text must contain 1 to 32 visible characters")
	}
	strokes := make([]Stroke, 0, len(runes)*3)
	for index, character := range runes {
		if character > 0x7f {
			return nil, errors.New("text supports ASCII letters, digits, space, and .,:;!?+-/_() only")
		}
		if character == ' ' {
			continue
		}
		glyph, ok := glyphs[unicode.ToUpper(character)]
		if !ok {
			return nil, errors.New("text supports ASCII letters, digits, space, and .,:;!?+-/_() only")
		}
		xOffset := float64(index) * 6
		for _, sourceStroke := range glyph {
			points := make(Stroke, len(sourceStroke))
			for pointIndex, point := range sourceStroke {
				points[pointIndex] = Point{X: xOffset + point.X, Y: point.Y}
			}
			strokes = append(strokes, points)
		}
	}
	if len(strokes) == 0 {
		return nil, errors.New("text must contain a visible character")
	}
	return strokes, nil
}
