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
const svgNamespace = "http://www.w3.org/2000/svg"

// CurveToleranceMM is the maximum supported deviation between an imported
// Bezier curve and its editable polyline approximation.
const CurveToleranceMM = 0.05

const maxFlattenDepth = 16
const maxGroupDepth = 16
const maxPathPoints = 4000
const roundingAllowanceMM = 0.001

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

// Parse accepts an SVG with a physical viewport, bounded nested groups, and
// supported static shapes. It maps SVG coordinates to lower-left-origin
// millimetres. Styling and machine operation settings are never imported, and
// external or active content is rejected.
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
	if root.Name.Local != "svg" || root.Name.Space != "" && root.Name.Space != svgNamespace {
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
	groupTransforms := []affine{}
	groupDepths := []int{}
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
			if item.Name.Space != "" && item.Name.Space != svgNamespace {
				return Document{}, fmt.Errorf("SVG element {%s}%s is not supported", item.Name.Space, item.Name.Local)
			}
			if item.Name.Local == "g" {
				if depth != 1+len(groupTransforms) {
					return Document{}, errors.New("nested SVG elements are not supported inside shapes")
				}
				if len(groupTransforms) >= maxGroupDepth {
					return Document{}, fmt.Errorf("SVG group nesting exceeds %d levels", maxGroupDepth)
				}
				values, err := attributes(item, map[string]bool{"id": true, "transform": true})
				if err != nil {
					return Document{}, err
				}
				groupTransform, err := parseTransform(values["transform"])
				if err != nil {
					return Document{}, err
				}
				if len(groupTransforms) > 0 {
					groupTransform = groupTransforms[len(groupTransforms)-1].multiply(groupTransform)
				}
				depth++
				groupDepths = append(groupDepths, depth)
				groupTransforms = append(groupTransforms, groupTransform)
				continue
			}
			if depth != 1+len(groupTransforms) {
				return Document{}, errors.New("nested SVG elements are not supported")
			}
			parentTransform := identityAffine()
			if len(groupTransforms) > 0 {
				parentTransform = groupTransforms[len(groupTransforms)-1]
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
				transformSpec = values["transform"]
				curveTransform, err := parseTransform(transformSpec)
				if err != nil {
					return Document{}, err
				}
				curveTransform = parentTransform.multiply(curveTransform)
				toMM := func(point Point) Point {
					return importPoint(point, curveTransform, viewBox, sx, sy, height)
				}
				subpaths, err = parsePath(values["d"], toMM, maxPathPoints-totalPoints)
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
			affine = parentTransform.multiply(affine)
			if len(subpaths) > 500 {
				return Document{}, errors.New("SVG path exceeds 500 subpaths")
			}
			for i := range subpaths {
				path := &subpaths[i]
				points := make([]Point, 0, len(path.Points))
				for _, point := range path.Points {
					transformed := importPoint(point, affine, viewBox, sx, sy, height)
					x, y := transformed.XMM, transformed.YMM
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
			if item.Name.Local == "g" {
				if len(groupDepths) == 0 || groupDepths[len(groupDepths)-1] != depth {
					return Document{}, errors.New("unexpected SVG group closing tag")
				}
				groupDepths = groupDepths[:len(groupDepths)-1]
				groupTransforms = groupTransforms[:len(groupTransforms)-1]
			}
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
		if attr.Name.Space != "" {
			return nil, fmt.Errorf("SVG attribute %q is not supported", attr.Name.Local)
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

func importPoint(point Point, transform affine, viewBox []float64, sx, sy, height float64) Point {
	transformed := transform.apply(point)
	return Point{XMM: (transformed.XMM - viewBox[0]) * sx, YMM: height - (transformed.YMM-viewBox[1])*sy}
}

func parsePath(source string, toMM func(Point) Point, maxPoints int) ([]Subpath, error) {
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
	lastCubicControl, lastQuadraticControl := Point{}, Point{}
	hasCubicControl, hasQuadraticControl := false, false
	command := byte(0)
	active := -1
	closedSinceMove := false
	emittedPoints := 0
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
	appendPoint := func(point Point) error {
		path := &subpaths[active]
		if len(path.Points) > 0 && path.Points[len(path.Points)-1] == point {
			return nil
		}
		if emittedPoints >= maxPoints {
			return errors.New("SVG path exceeds remaining 4000-point import budget")
		}
		path.Points = append(path.Points, point)
		emittedPoints++
		return nil
	}
	for index < len(tokens) {
		if len(tokens[index]) == 1 && isLetter(tokens[index][0]) {
			command = tokens[index][0]
			index++
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
			hasCubicControl, hasQuadraticControl = false, false
			command = 0
			continue
		}
		if cmd != 'M' && cmd != 'L' && cmd != 'H' && cmd != 'V' && cmd != 'C' && cmd != 'S' && cmd != 'Q' && cmd != 'T' {
			return nil, fmt.Errorf("path command %c is not supported; use M/L/H/V/C/S/Q/T/Z commands", command)
		}
		if cmd != 'M' && closedSinceMove {
			return nil, errors.New("a path needs a new M command after Z before drawing again")
		}
		first := true
		for index < len(tokens) && !isCommandToken(tokens[index]) {
			previous := current
			switch cmd {
			case 'M', 'L':
				hasCubicControl, hasQuadraticControl = false, false
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
				hasCubicControl, hasQuadraticControl = false, false
				x, err := readNumber()
				if err != nil {
					return nil, err
				}
				if !upper {
					x += current.XMM
				}
				current.XMM = x
			case 'V':
				hasCubicControl, hasQuadraticControl = false, false
				y, err := readNumber()
				if err != nil {
					return nil, err
				}
				if !upper {
					y += current.YMM
				}
				current.YMM = y
			case 'Q':
				if active < 0 {
					return nil, errors.New("path curve command must follow a move")
				}
				controlX, err := readNumber()
				if err != nil {
					return nil, err
				}
				controlY, err := readNumber()
				if err != nil {
					return nil, err
				}
				x, err := readNumber()
				if err != nil {
					return nil, err
				}
				y, err := readNumber()
				if err != nil {
					return nil, err
				}
				if !upper {
					controlX, controlY = controlX+previous.XMM, controlY+previous.YMM
					x, y = x+previous.XMM, y+previous.YMM
				}
				control, endpoint := Point{XMM: controlX, YMM: controlY}, Point{XMM: x, YMM: y}
				if !finitePoint(control) || !finitePoint(endpoint) {
					return nil, errors.New("path curve coordinate overflow")
				}
				if err := flattenQuadratic(previous, control, endpoint, toMM, 0, func(point Point) error { return appendPoint(point) }); err != nil {
					return nil, err
				}
				lastQuadraticControl, hasQuadraticControl = control, true
				hasCubicControl = false
				current = endpoint
			case 'T':
				if active < 0 {
					return nil, errors.New("path curve command must follow a move")
				}
				x, err := readNumber()
				if err != nil {
					return nil, err
				}
				y, err := readNumber()
				if err != nil {
					return nil, err
				}
				if !upper {
					x, y = x+previous.XMM, y+previous.YMM
				}
				control := previous
				if hasQuadraticControl {
					control = Point{XMM: 2*previous.XMM - lastQuadraticControl.XMM, YMM: 2*previous.YMM - lastQuadraticControl.YMM}
				}
				endpoint := Point{XMM: x, YMM: y}
				if !finitePoint(control) || !finitePoint(endpoint) {
					return nil, errors.New("path curve coordinate overflow")
				}
				if err := flattenQuadratic(previous, control, endpoint, toMM, 0, func(point Point) error { return appendPoint(point) }); err != nil {
					return nil, err
				}
				lastQuadraticControl, hasQuadraticControl = control, true
				hasCubicControl = false
				current = endpoint
			case 'C':
				if active < 0 {
					return nil, errors.New("path curve command must follow a move")
				}
				values := [6]float64{}
				for i := range values {
					value, err := readNumber()
					if err != nil {
						return nil, err
					}
					values[i] = value
				}
				if !upper {
					for i := range values {
						if i%2 == 0 {
							values[i] += previous.XMM
						} else {
							values[i] += previous.YMM
						}
					}
				}
				control1 := Point{XMM: values[0], YMM: values[1]}
				control2 := Point{XMM: values[2], YMM: values[3]}
				endpoint := Point{XMM: values[4], YMM: values[5]}
				if !finitePoint(control1) || !finitePoint(control2) || !finitePoint(endpoint) {
					return nil, errors.New("path curve coordinate overflow")
				}
				if err := flattenCubic(previous, control1, control2, endpoint, toMM, 0, func(point Point) error { return appendPoint(point) }); err != nil {
					return nil, err
				}
				lastCubicControl, hasCubicControl = control2, true
				hasQuadraticControl = false
				current = endpoint
			case 'S':
				if active < 0 {
					return nil, errors.New("path curve command must follow a move")
				}
				values := [4]float64{}
				for i := range values {
					value, err := readNumber()
					if err != nil {
						return nil, err
					}
					values[i] = value
				}
				if !upper {
					for i := range values {
						if i%2 == 0 {
							values[i] += previous.XMM
						} else {
							values[i] += previous.YMM
						}
					}
				}
				control1 := previous
				if hasCubicControl {
					control1 = Point{XMM: 2*previous.XMM - lastCubicControl.XMM, YMM: 2*previous.YMM - lastCubicControl.YMM}
				}
				control2 := Point{XMM: values[0], YMM: values[1]}
				endpoint := Point{XMM: values[2], YMM: values[3]}
				if !finitePoint(control1) || !finitePoint(control2) || !finitePoint(endpoint) {
					return nil, errors.New("path curve coordinate overflow")
				}
				if err := flattenCubic(previous, control1, control2, endpoint, toMM, 0, func(point Point) error { return appendPoint(point) }); err != nil {
					return nil, err
				}
				lastCubicControl, hasCubicControl = control2, true
				hasQuadraticControl = false
				current = endpoint
			}
			if !finite(current.XMM) || !finite(current.YMM) {
				return nil, errors.New("path coordinate overflow")
			}
			if cmd == 'M' && first {
				subpaths = append(subpaths, Subpath{})
				active, start = len(subpaths)-1, current
				if err := appendPoint(current); err != nil {
					return nil, err
				}
				closedSinceMove = false
			} else if cmd != 'C' && cmd != 'S' && cmd != 'Q' && cmd != 'T' {
				if active < 0 {
					return nil, errors.New("path line command must follow a move")
				}
				if err := appendPoint(current); err != nil {
					return nil, err
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

func finitePoint(point Point) bool { return finite(point.XMM) && finite(point.YMM) }

func flattenQuadratic(start, control, end Point, toMM func(Point) Point, depth int, append func(Point) error) error {
	if !finitePoint(toMM(start)) || !finitePoint(toMM(control)) || !finitePoint(toMM(end)) {
		return errors.New("path curve coordinate overflow")
	}
	if distanceToSegment(toMM(control), toMM(start), toMM(end)) <= CurveToleranceMM-roundingAllowanceMM {
		return append(end)
	}
	if depth >= maxFlattenDepth {
		return errors.New("SVG curve cannot meet the 0.05 mm chord tolerance within the subdivision limit")
	}
	startControl := midpoint(start, control)
	controlEnd := midpoint(control, end)
	middle := midpoint(startControl, controlEnd)
	if err := flattenQuadratic(start, startControl, middle, toMM, depth+1, append); err != nil {
		return err
	}
	return flattenQuadratic(middle, controlEnd, end, toMM, depth+1, append)
}

func flattenCubic(start, control1, control2, end Point, toMM func(Point) Point, depth int, append func(Point) error) error {
	mappedStart, mappedControl1 := toMM(start), toMM(control1)
	mappedControl2, mappedEnd := toMM(control2), toMM(end)
	if !finitePoint(mappedStart) || !finitePoint(mappedControl1) || !finitePoint(mappedControl2) || !finitePoint(mappedEnd) {
		return errors.New("path curve coordinate overflow")
	}
	flatness := math.Max(distanceToSegment(mappedControl1, mappedStart, mappedEnd), distanceToSegment(mappedControl2, mappedStart, mappedEnd))
	if flatness <= CurveToleranceMM-roundingAllowanceMM {
		return append(end)
	}
	if depth >= maxFlattenDepth {
		return errors.New("SVG curve cannot meet the 0.05 mm chord tolerance within the subdivision limit")
	}
	startControl1 := midpoint(start, control1)
	control1Control2 := midpoint(control1, control2)
	control2End := midpoint(control2, end)
	leftControl2 := midpoint(startControl1, control1Control2)
	rightControl1 := midpoint(control1Control2, control2End)
	middle := midpoint(leftControl2, rightControl1)
	if err := flattenCubic(start, startControl1, leftControl2, middle, toMM, depth+1, append); err != nil {
		return err
	}
	return flattenCubic(middle, rightControl1, control2End, end, toMM, depth+1, append)
}

func midpoint(first, second Point) Point {
	return Point{XMM: (first.XMM + second.XMM) / 2, YMM: (first.YMM + second.YMM) / 2}
}

func distanceToSegment(point, start, end Point) float64 {
	dx, dy := end.XMM-start.XMM, end.YMM-start.YMM
	lengthSquared := dx*dx + dy*dy
	if lengthSquared == 0 {
		return math.Hypot(point.XMM-start.XMM, point.YMM-start.YMM)
	}
	t := ((point.XMM-start.XMM)*dx + (point.YMM-start.YMM)*dy) / lengthSquared
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(point.XMM-(start.XMM+t*dx), point.YMM-(start.YMM+t*dy))
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
