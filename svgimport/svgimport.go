// Package svgimport converts a bounded, static SVG subset into editable millimetre paths.
package svgimport

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const MaxBytes = 1 << 20

var transformToken = regexp.MustCompile(`([A-Za-z]+)\s*\(([^)]*)\)`)
var numberToken = regexp.MustCompile(`[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?`)
var pathToken = regexp.MustCompile(`[A-Za-z]|[-+]?(?:[0-9]*\.)?[0-9]+(?:[eE][-+]?[0-9]+)?`)

type Point struct {
	XMM float64 `json:"x_mm"`
	YMM float64 `json:"y_mm"`
}

type Subpath struct {
	Points []Point `json:"points"`
	Closed bool    `json:"closed"`
}

type Object struct {
	Subpaths []Subpath `json:"subpaths"`
}

type Document struct {
	WidthMM  float64  `json:"width_mm"`
	HeightMM float64  `json:"height_mm"`
	Objects  []Object `json:"objects"`
}

// Parse accepts an SVG with a physical viewport and supported direct-child shapes.
// It maps SVG coordinates to lower-left-origin millimetres. Styling and machine
// operation settings are never imported, and external or active content is rejected.
func Parse(source []byte) (Document, error) {
	if len(source) == 0 || len(source) > MaxBytes {
		return Document{}, errors.New("SVG must contain 1 byte to 1 MiB")
	}
	decoder := xml.NewDecoder(strings.NewReader(string(source)))
	decoder.Strict = true
	var root xml.StartElement
	for {
		token, err := decoder.Token()
		if err != nil {
			return Document{}, fmt.Errorf("read SVG root: %w", err)
		}
		if start, ok := token.(xml.StartElement); ok {
			root = start
			break
		}
		if directive, ok := token.(xml.Directive); ok {
			return Document{}, fmt.Errorf("SVG directives are not supported: %s", directive)
		}
	}
	if root.Name.Local != "svg" {
		return Document{}, errors.New("root element must be svg")
	}
	attrs, err := attributes(root, map[string]bool{"xmlns": true, "id": true, "version": true, "width": true, "height": true, "viewBox": true, "preserveAspectRatio": true})
	if err != nil {
		return Document{}, err
	}
	width, err := physicalLength(attrs["width"])
	if err != nil {
		return Document{}, fmt.Errorf("SVG width: %w", err)
	}
	height, err := physicalLength(attrs["height"])
	if err != nil {
		return Document{}, fmt.Errorf("SVG height: %w", err)
	}
	viewBox, err := parseNumberList(attrs["viewBox"], 4)
	if err != nil || viewBox[2] <= 0 || viewBox[3] <= 0 {
		return Document{}, errors.New("SVG viewBox must contain four finite numbers with positive width and height")
	}
	if attrs["preserveAspectRatio"] != "" && attrs["preserveAspectRatio"] != "none" {
		return Document{}, errors.New("SVG preserveAspectRatio must be none or omitted with a matching viewBox ratio")
	}
	sx, sy := width/viewBox[2], height/viewBox[3]
	if attrs["preserveAspectRatio"] != "none" && math.Abs(sx-sy) > math.Max(sx, sy)*1e-9 {
		return Document{}, errors.New("SVG viewport and viewBox ratios differ; use preserveAspectRatio=none explicitly")
	}
	doc := Document{WidthMM: width, HeightMM: height, Objects: []Object{}}
	depth, totalPoints := 1, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Document{}, fmt.Errorf("read SVG content: %w", err)
		}
		switch item := token.(type) {
		case xml.StartElement:
			if depth != 1 {
				return Document{}, errors.New("nested SVG elements are not supported")
			}
			var coords []float64
			var transformSpec string
			var subpaths []Subpath
			closed := false
			switch item.Name.Local {
			case "path":
				values, err := attributes(item, map[string]bool{"d": true, "id": true, "transform": true})
				if err != nil {
					return Document{}, err
				}
				subpaths, err = parsePath(values["d"])
				if err != nil {
					return Document{}, err
				}
			case "circle", "ellipse":
				allowed := map[string]bool{"cx": true, "cy": true, "id": true, "transform": true}
				if item.Name.Local == "circle" {
					allowed["r"] = true
				} else {
					allowed["rx"], allowed["ry"] = true, true
				}
				values, err := attributes(item, allowed)
				if err != nil {
					return Document{}, err
				}
				cx, cy := 0.0, 0.0
				if values["cx"] != "" {
					cx, err = finiteNumber(values["cx"])
					if err != nil {
						return Document{}, errors.New("circle center must be finite")
					}
				}
				if values["cy"] != "" {
					cy, err = finiteNumber(values["cy"])
					if err != nil {
						return Document{}, errors.New("circle center must be finite")
					}
				}
				rx, ry := 0.0, 0.0
				if item.Name.Local == "circle" {
					rx, err = finiteNumber(values["r"])
					ry = rx
				} else {
					rx, err = finiteNumber(values["rx"])
					if err == nil {
						ry, err = finiteNumber(values["ry"])
					}
				}
				if err != nil || rx <= 0 || ry <= 0 {
					return Document{}, errors.New("circle or ellipse radii must be positive")
				}
				points := make([]Point, 32)
				for i := range points {
					angle := float64(i) * 2 * math.Pi / float64(len(points))
					points[i] = Point{XMM: cx + math.Cos(angle)*rx, YMM: cy + math.Sin(angle)*ry}
				}
				subpaths = []Subpath{{Points: points, Closed: true}}
			case "rect":
				values, err := attributes(item, map[string]bool{"x": true, "y": true, "width": true, "height": true, "id": true, "transform": true})
				if err != nil {
					return Document{}, err
				}
				var x, y float64
				if values["x"] != "" {
					x, err = finiteNumber(values["x"])
				}
				if err != nil {
					return Document{}, errors.New("rectangle x must be a finite number")
				}
				if values["y"] != "" {
					y, err = finiteNumber(values["y"])
				}
				if err != nil {
					return Document{}, errors.New("rectangle y must be a finite number")
				}
				w, err := finiteNumber(values["width"])
				if err != nil || w <= 0 {
					return Document{}, errors.New("rectangle width must be positive")
				}
				h, err := finiteNumber(values["height"])
				if err != nil || h <= 0 {
					return Document{}, errors.New("rectangle height must be positive")
				}
				subpaths = []Subpath{{Closed: true, Points: []Point{{XMM: x, YMM: y}, {XMM: x + w, YMM: y}, {XMM: x + w, YMM: y + h}, {XMM: x, YMM: y + h}}}}
			case "polyline", "polygon":
				values, err := attributes(item, map[string]bool{"points": true, "id": true, "transform": true})
				if err != nil {
					return Document{}, err
				}
				coords, err = parseNumberList(values["points"], 0)
				minimum := 4
				if item.Name.Local == "polygon" {
					minimum = 6
				}
				if err != nil || len(coords) < minimum || len(coords)%2 != 0 {
					return Document{}, errors.New("polyline needs two or polygon needs three finite coordinate pairs")
				}
				closed = item.Name.Local == "polygon"
			case "line":
				values, err := attributes(item, map[string]bool{"x1": true, "y1": true, "x2": true, "y2": true, "id": true, "transform": true})
				if err != nil {
					return Document{}, err
				}
				coords = make([]float64, 4)
				for i, key := range []string{"x1", "y1", "x2", "y2"} {
					coords[i], err = strconv.ParseFloat(values[key], 64)
					if err != nil || !finite(coords[i]) {
						return Document{}, fmt.Errorf("line attribute %s must be a finite number", key)
					}
				}
			default:
				return Document{}, fmt.Errorf("SVG element <%s> is not supported", item.Name.Local)
			}
			if len(subpaths) == 0 {
				points := make([]Point, 0, len(coords)/2)
				for i := 0; i < len(coords); i += 2 {
					points = append(points, Point{XMM: coords[i], YMM: coords[i+1]})
				}
				subpaths = []Subpath{{Points: points, Closed: closed}}
			}
			for _, attr := range item.Attr {
				if attr.Name.Local == "transform" {
					transformSpec = attr.Value
				}
			}
			affine, err := parseTransform(transformSpec)
			if err != nil {
				return Document{}, err
			}
			if len(subpaths) > 500 {
				return Document{}, errors.New("SVG path exceeds 500 subpaths")
			}
			for i := range subpaths {
				path := &subpaths[i]
				points := make([]Point, 0, len(path.Points))
				for _, point := range path.Points {
					transformed := affine.apply(point)
					x := (transformed.XMM - viewBox[0]) * sx
					y := height - (transformed.YMM-viewBox[1])*sy
					if !finite(x) || !finite(y) || math.Abs(x) > 10000 || math.Abs(y) > 10000 {
						return Document{}, errors.New("SVG point exceeds the 10000 mm import range")
					}
					point = Point{XMM: roundMM(x), YMM: roundMM(y)}
					if len(points) == 0 || points[len(points)-1] != point {
						points = append(points, point)
					}
				}
				if path.Closed && len(points) > 1 && points[0] == points[len(points)-1] {
					points = points[:len(points)-1]
				}
				minimum := 2
				if path.Closed {
					minimum = 3
				}
				if len(points) < minimum {
					return Document{}, errors.New("SVG path collapses below the minimum distinct points after millimetre conversion")
				}
				totalPoints += len(points)
				if totalPoints > 4000 {
					return Document{}, errors.New("SVG import exceeds 4000 points")
				}
				path.Points = points
			}
			doc.Objects = append(doc.Objects, Object{Subpaths: subpaths})
			depth++
		case xml.EndElement:
			depth--
			if depth < 0 {
				return Document{}, errors.New("unexpected SVG closing tag")
			}
		case xml.Directive:
			return Document{}, errors.New("SVG directives and entities are not supported")
		case xml.ProcInst:
			return Document{}, errors.New("SVG processing instructions are not supported")
		case xml.CharData:
			if strings.TrimSpace(string(item)) != "" {
				return Document{}, errors.New("text content is not supported in static SVG imports")
			}
		}
		if len(doc.Objects) > 500 {
			return Document{}, errors.New("SVG import exceeds 500 paths")
		}
	}
	if len(doc.Objects) == 0 {
		return Document{}, errors.New("SVG contains no supported paths")
	}
	return doc, nil
}

func attributes(element xml.StartElement, allowed map[string]bool) (map[string]string, error) {
	values := make(map[string]string, len(element.Attr))
	for _, attr := range element.Attr {
		if attr.Name.Space == "xmlns" || attr.Name.Space == "" && attr.Name.Local == "xmlns" {
			continue
		}
		name := attr.Name.Local
		if strings.HasPrefix(strings.ToLower(name), "on") || !allowed[name] {
			return nil, fmt.Errorf("SVG attribute %q is not supported", name)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("SVG attribute %q is repeated", name)
		}
		values[name] = attr.Value
	}
	return values, nil
}

func physicalLength(value string) (float64, error) {
	value = strings.TrimSpace(value)
	unit := "px"
	for _, suffix := range []string{"mm", "cm", "in", "pt", "pc", "px"} {
		if strings.HasSuffix(value, suffix) {
			unit, value = suffix, strings.TrimSpace(strings.TrimSuffix(value, suffix))
			break
		}
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || !finite(number) || number <= 0 {
		return 0, errors.New("must be a positive finite CSS length")
	}
	switch unit {
	case "mm":
	case "cm":
		number *= 10
	case "in":
		number *= 25.4
	case "pt":
		number *= 25.4 / 72
	case "pc":
		number *= 25.4 / 6
	case "px":
		number *= 25.4 / 96
	}
	if !finite(number) || number > 10000 {
		return 0, errors.New("must not exceed 10000 mm")
	}
	return roundMM(number), nil
}

func parseNumberList(source string, exact int) ([]float64, error) {
	values := make([]float64, 0, 8)
	index := 0
	for index < len(source) {
		spaced := false
		for index < len(source) && (source[index] == ' ' || source[index] == '\n' || source[index] == '\t' || source[index] == '\r') {
			index++
			spaced = true
		}
		if index >= len(source) {
			break
		}
		if source[index] == ',' {
			if len(values) == 0 {
				return nil, errors.New("invalid number list")
			}
			index++
			for index < len(source) && (source[index] == ' ' || source[index] == '\n' || source[index] == '\t' || source[index] == '\r') {
				index++
			}
			if index >= len(source) || source[index] == ',' {
				return nil, errors.New("invalid number list")
			}
		} else if len(values) > 0 && !spaced && source[index] != '+' && source[index] != '-' {
			return nil, errors.New("invalid number separator")
		}
		match := numberToken.FindString(source[index:])
		if match == "" {
			return nil, errors.New("invalid number list")
		}
		value, err := strconv.ParseFloat(match, 64)
		if err != nil || !finite(value) {
			return nil, errors.New("invalid finite number")
		}
		values = append(values, value)
		index += len(match)
		if index < len(source) && source[index] != ',' && source[index] != ' ' && source[index] != '\n' && source[index] != '\t' && source[index] != '\r' && source[index] != '+' && source[index] != '-' {
			return nil, errors.New("invalid number separator")
		}
	}
	if (exact > 0 && len(values) != exact) || len(values) == 0 {
		return nil, errors.New("invalid number list")
	}
	return values, nil
}

func finite(value float64) bool     { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func roundMM(value float64) float64 { return math.Round(value*1000) / 1000 }

func parsePath(source string) ([]Subpath, error) {
	tokens := pathToken.FindAllString(source, -1)
	if len(tokens) == 0 {
		return nil, errors.New("path d must contain supported line commands")
	}
	if len(tokens) > 12000 {
		return nil, errors.New("path d exceeds the 4000-point import limit")
	}
	residue := pathToken.ReplaceAllString(source, "")
	if strings.Trim(residue, " ,\t\r\n") != "" {
		return nil, errors.New("path d contains malformed data")
	}
	subpaths := []Subpath{}
	current := Point{}
	start := Point{}
	command := byte(0)
	active := -1
	closedSinceMove := false
	index := 0
	readNumber := func() (float64, error) {
		if index >= len(tokens) || len(tokens[index]) == 1 && ((tokens[index][0] >= 'A' && tokens[index][0] <= 'Z') || (tokens[index][0] >= 'a' && tokens[index][0] <= 'z')) {
			return 0, errors.New("path command is missing coordinates")
		}
		value, err := strconv.ParseFloat(tokens[index], 64)
		index++
		if err != nil || !finite(value) || math.Abs(value) > 1e7 {
			return 0, errors.New("path coordinate is invalid or too large")
		}
		return value, nil
	}
	for index < len(tokens) {
		if len(tokens[index]) == 1 && isLetter(tokens[index][0]) {
			command = tokens[index][0]
			index++
			if strings.ContainsRune("CcSsQqTtAa", rune(command)) {
				return nil, fmt.Errorf("path command %c is not supported; use M/L/H/V/Z lines", command)
			}
		}
		if command == 0 {
			return nil, errors.New("path must start with a move command")
		}
		upper := command >= 'A' && command <= 'Z'
		cmd := command
		if !upper {
			cmd -= 'a' - 'A'
		}
		if cmd == 'Z' {
			if active < 0 || len(subpaths[active].Points) < 3 {
				return nil, errors.New("closed paths need at least three points")
			}
			subpaths[active].Closed = true
			current = start
			closedSinceMove = true
			command = 0
			continue
		}
		if cmd != 'M' && cmd != 'L' && cmd != 'H' && cmd != 'V' {
			return nil, fmt.Errorf("path command %c is not supported; use M/L/H/V/Z lines", command)
		}
		if cmd != 'M' && closedSinceMove {
			return nil, errors.New("a path needs a new M command after Z before drawing again")
		}
		first := true
		for index < len(tokens) && !isCommandToken(tokens[index]) {
			previous := current
			switch cmd {
			case 'M', 'L':
				x, err := readNumber()
				if err != nil {
					return nil, err
				}
				y, err := readNumber()
				if err != nil {
					return nil, err
				}
				if !upper {
					x += current.XMM
					y += current.YMM
				}
				current = Point{XMM: x, YMM: y}
			case 'H':
				x, err := readNumber()
				if err != nil {
					return nil, err
				}
				if !upper {
					x += current.XMM
				}
				current.XMM = x
			case 'V':
				y, err := readNumber()
				if err != nil {
					return nil, err
				}
				if !upper {
					y += current.YMM
				}
				current.YMM = y
			}
			if !finite(current.XMM) || !finite(current.YMM) {
				return nil, errors.New("path coordinate overflow")
			}
			if cmd == 'M' && first {
				subpaths = append(subpaths, Subpath{Points: []Point{current}})
				active, start = len(subpaths)-1, current
				closedSinceMove = false
			} else {
				if active < 0 {
					return nil, errors.New("path line command must follow a move")
				}
				if current != previous {
					subpaths[active].Points = append(subpaths[active].Points, current)
				}
			}
			first = false
			if cmd == 'M' {
				command = 'L'
				cmd = 'L'
			}
		}
		if first {
			return nil, errors.New("path command is missing coordinates")
		}
	}
	for _, path := range subpaths {
		if len(path.Points) < 2 || path.Closed && len(path.Points) < 3 {
			return nil, errors.New("path contains too few distinct points")
		}
	}
	return subpaths, nil
}

func finiteNumber(source string) (float64, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(source), 64)
	if err != nil || !finite(value) || math.Abs(value) > 1e7 {
		return 0, errors.New("invalid coordinate")
	}
	return value, nil
}

func isLetter(value byte) bool         { return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' }
func isCommandToken(token string) bool { return len(token) == 1 && isLetter(token[0]) }

type affine struct{ a, b, c, d, e, f float64 }

func identityAffine() affine { return affine{a: 1, d: 1} }
func (left affine) multiply(right affine) affine {
	return affine{
		a: left.a*right.a + left.c*right.b, b: left.b*right.a + left.d*right.b,
		c: left.a*right.c + left.c*right.d, d: left.b*right.c + left.d*right.d,
		e: left.a*right.e + left.c*right.f + left.e, f: left.b*right.e + left.d*right.f + left.f,
	}
}
func (matrix affine) apply(point Point) Point {
	return Point{XMM: matrix.a*point.XMM + matrix.c*point.YMM + matrix.e, YMM: matrix.b*point.XMM + matrix.d*point.YMM + matrix.f}
}
func parseTransform(source string) (affine, error) {
	result := identityAffine()
	if strings.TrimSpace(source) == "" {
		return result, nil
	}
	matches := transformToken.FindAllStringSubmatchIndex(source, -1)
	if len(matches) == 0 {
		return affine{}, errors.New("SVG transform is malformed")
	}
	var residue strings.Builder
	last := 0
	for _, match := range matches {
		residue.WriteString(source[last:match[0]])
		last = match[1]
	}
	residue.WriteString(source[last:])
	if strings.Trim(residue.String(), " ,\t\r\n") != "" {
		return affine{}, errors.New("SVG transform is malformed")
	}
	for _, match := range matches {
		name := source[match[2]:match[3]]
		values, err := parseNumberList(source[match[4]:match[5]], 0)
		if err != nil {
			return affine{}, errors.New("SVG transform parameters are invalid")
		}
		operation := identityAffine()
		switch name {
		case "matrix":
			if len(values) != 6 {
				return affine{}, errors.New("matrix() needs six values")
			}
			operation = affine{a: values[0], b: values[1], c: values[2], d: values[3], e: values[4], f: values[5]}
		case "translate":
			if len(values) < 1 || len(values) > 2 {
				return affine{}, errors.New("translate() needs one or two values")
			}
			operation.e = values[0]
			if len(values) == 2 {
				operation.f = values[1]
			}
		case "scale":
			if len(values) < 1 || len(values) > 2 {
				return affine{}, errors.New("scale() needs one or two values")
			}
			operation.a = values[0]
			operation.d = values[0]
			if len(values) == 2 {
				operation.d = values[1]
			}
		case "rotate":
			if len(values) != 1 && len(values) != 3 {
				return affine{}, errors.New("rotate() needs one or three values")
			}
			angle := values[0] * math.Pi / 180
			cosine, sine := math.Cos(angle), math.Sin(angle)
			operation = affine{a: cosine, b: sine, c: -sine, d: cosine}
			if len(values) == 3 {
				operation.e = values[1] - cosine*values[1] + sine*values[2]
				operation.f = values[2] - sine*values[1] - cosine*values[2]
			}
		default:
			return affine{}, fmt.Errorf("SVG transform %s() is not supported", name)
		}
		if !finite(operation.a) || !finite(operation.b) || !finite(operation.c) || !finite(operation.d) || !finite(operation.e) || !finite(operation.f) {
			return affine{}, errors.New("SVG transform exceeds the numeric range")
		}
		result = result.multiply(operation)
	}
	determinant := result.a*result.d - result.b*result.c
	if !finite(determinant) || math.Abs(determinant) < 1e-12 {
		return affine{}, errors.New("SVG transform must not collapse geometry")
	}
	return result, nil
}
