package server

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/ConstructorFleet/emberdeck/internal/monoline"
)

const monolineFontID = monoline.FontID
const monolineTextMaxRunes = monoline.MaxRunes
const monolineTextMinSizeMM = 1
const monolineTextMaxSizeMM = 100

type VectorText struct {
	Content string  `json:"content"`
	Font    string  `json:"font"`
	SizeMM  float64 `json:"size_mm"`
	XMM     float64 `json:"x_mm"`
	YMM     float64 `json:"y_mm"`
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
	strokes, err := monoline.Outline(source.Content)
	if err != nil {
		return nil, err
	}
	unit := source.SizeMM / 7
	paths := make([]VectorSubpath, 0, len(strokes))
	for _, glyphStroke := range strokes {
		points := make([]VectorPoint, len(glyphStroke))
		for pointIndex, point := range glyphStroke {
			points[pointIndex] = VectorPoint{
				XMM: roundTextMM(source.XMM + point.X*unit),
				YMM: roundTextMM(source.YMM + point.Y*unit),
			}
		}
		paths = append(paths, VectorSubpath{Points: points, Closed: false})
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
