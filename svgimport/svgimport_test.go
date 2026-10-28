package svgimport_test

import (
	"math"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/svgimport"
)

func TestParseConvertsViewBoxPolylineToLowerLeftMillimetres(t *testing.T) {
	source := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="40mm" height="20mm" viewBox="10 20 40 20"><polyline points="10,20 50,40"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	if got.WidthMM != 40 || got.HeightMM != 20 || len(got.Objects) != 1 || len(got.Objects[0].Subpaths) != 1 {
		t.Fatalf("parsed SVG dimensions and objects = %#v", got)
	}
	path := got.Objects[0].Subpaths[0]
	want := []svgimport.Point{{XMM: 0, YMM: 20}, {XMM: 40, YMM: 0}}
	if path.Closed || len(path.Points) != len(want) {
		t.Fatalf("parsed subpath = %#v", path)
	}
	for i := range want {
		if path.Points[i] != want[i] {
			t.Fatalf("point %d = %#v, want %#v", i, path.Points[i], want[i])
		}
	}
}

func TestParseRejectsActiveExternalAndUnmodeledSVGContent(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{"script", `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><script>alert(1)</script></svg>`},
		{"event attribute", `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1" onload="x"/></svg>`},
		{"external reference", `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1" href="https://example.invalid/x"/></svg>`},
		{"nested child", `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1"><polyline points="1,1 2,2"/></polyline></svg>`},
		{"doctype", `<!DOCTYPE svg [<!ENTITY x "x">]><svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1"/></svg>`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := svgimport.Parse([]byte(test.source)); err == nil {
				t.Fatal("unsafe or unsupported SVG content was accepted")
			}
		})
	}
}

func TestParseComposesNestedGroupAndElementTransforms(t *testing.T) {
	source := []byte(`<svg width="100mm" height="100mm" viewBox="0 0 100 100"><g transform="translate(10 20)"><g transform="scale(2)"><g transform="rotate(90)"><g transform="matrix(1 0 0 1 3 4)"><polyline points="1,2 3,4" transform="translate(5 0)"/></g></g></g></g></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Objects) != 1 || len(got.Objects[0].Subpaths) != 1 {
		t.Fatalf("nested SVG objects = %#v", got.Objects)
	}
	want := []svgimport.Point{{XMM: -2, YMM: 62}, {XMM: -6, YMM: 58}}
	points := got.Objects[0].Subpaths[0].Points
	if len(points) != len(want) {
		t.Fatalf("nested SVG points = %#v, want %#v", points, want)
	}
	for i := range want {
		if points[i] != want[i] {
			t.Fatalf("point %d = %#v, want %#v", i, points[i], want[i])
		}
	}
}

func TestParseRejectsMalformedOrUnsupportedGroups(t *testing.T) {
	deepGroups := strings.Repeat(`<g>`, 17) + `<line x1="0" y1="0" x2="1" y2="1"/>` + strings.Repeat(`</g>`, 17)
	for name, content := range map[string]string{
		"malformed transform": `<g transform="translate(1,)"><line x1="0" y1="0" x2="1" y2="1"/></g>`,
		"unsupported style":   `<g style="fill:red"><line x1="0" y1="0" x2="1" y2="1"/></g>`,
		"event attribute":     `<g onload="run()"><line x1="0" y1="0" x2="1" y2="1"/></g>`,
		"unsupported child":   `<g><switch><line x1="0" y1="0" x2="1" y2="1"/></switch></g>`,
		"foreign namespace":   `<x:g xmlns:x="urn:foreign"><line x1="0" y1="0" x2="1" y2="1"/></x:g>`,
		"group depth limit":   deepGroups,
		"malformed nesting":   `<g><line x1="0" y1="0" x2="1" y2="1"></g>`,
	} {
		t.Run(name, func(t *testing.T) {
			source := []byte(`<svg width="40mm" height="20mm" viewBox="0 0 40 20">` + content + `</svg>`)
			if _, err := svgimport.Parse(source); err == nil {
				t.Fatal("malformed or unsupported group was accepted")
			}
		})
	}
}

func TestParseConvertsPolygonToClosedLowerLeftPath(t *testing.T) {
	source := []byte(`<svg width="25.4mm" height="25.4mm" viewBox="0 0 96 96"><polygon points="0,0 96,0 48,96"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Objects) != 1 || len(got.Objects[0].Subpaths) != 1 {
		t.Fatalf("parsed objects = %#v", got.Objects)
	}
	path := got.Objects[0].Subpaths[0]
	want := []svgimport.Point{{XMM: 0, YMM: 25.4}, {XMM: 25.4, YMM: 25.4}, {XMM: 12.7, YMM: 0}}
	if !path.Closed || len(path.Points) != len(want) {
		t.Fatalf("parsed polygon = %#v", path)
	}
	for i := range want {
		if path.Points[i] != want[i] {
			t.Fatalf("point %d = %#v, want %#v", i, path.Points[i], want[i])
		}
	}
}

func TestParseConvertsRelativeLinePathCommands(t *testing.T) {
	source := []byte(`<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M10 20 l10 0 v-10 h-10 z"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	want := []svgimport.Point{{XMM: 10, YMM: 0}, {XMM: 20, YMM: 0}, {XMM: 20, YMM: 10}, {XMM: 10, YMM: 10}}
	if !path.Closed || len(path.Points) != len(want) {
		t.Fatalf("parsed path = %#v", path)
	}
	for i := range want {
		if path.Points[i] != want[i] {
			t.Fatalf("point %d = %#v, want %#v", i, path.Points[i], want[i])
		}
	}
}

func TestParseFlattensQuadraticPathWithinPhysicalTolerance(t *testing.T) {
	source := []byte(`<svg width="100mm" height="100mm" viewBox="0 0 100 100"><path d="M0 0 Q50 100 100 0"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	if path.Closed || len(path.Points) < 3 || len(path.Points) > 4000 {
		t.Fatalf("flattened quadratic path has closed=%v and %d points; want a bounded open approximation", path.Closed, len(path.Points))
	}
	if path.Points[0] != (svgimport.Point{XMM: 0, YMM: 100}) || path.Points[len(path.Points)-1] != (svgimport.Point{XMM: 100, YMM: 100}) {
		t.Fatalf("flattened quadratic endpoints = %#v, want lower-left converted source endpoints", path.Points)
	}
	for sample := 0; sample <= 1000; sample++ {
		testT := float64(sample) / 1000
		x := 100 * testT
		y := 100 - 200*testT*(1-testT)
		if distanceToPolyline(x, y, path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("sample at t=%.3f is farther than the documented curve tolerance from the imported path", testT)
		}
	}
}

func TestParseFlattensCubicAndRelativeQuadraticPathCommands(t *testing.T) {
	source := []byte(`<svg width="140mm" height="100mm" viewBox="0 0 140 100"><path d="M10 80 C20 100 40 100 50 80 c10 -20 20 -20 30 0 q10 20 20 0 10 -20 20 0"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	if path.Closed || len(path.Points) < 8 {
		t.Fatalf("cubic and quadratic commands produced an unexpected path: %#v", path)
	}
	if first, last := path.Points[0], path.Points[len(path.Points)-1]; first != (svgimport.Point{XMM: 10, YMM: 20}) || last != (svgimport.Point{XMM: 120, YMM: 20}) {
		t.Fatalf("path endpoints = %#v and %#v, want (10,20) and (120,20)", first, last)
	}
	for sample := 0; sample <= 1000; sample++ {
		testT := float64(sample) / 1000
		if distanceToPolyline(cubicCoordinate(10, 20, 40, 50, testT), cubicCoordinate(20, 0, 0, 20, testT), path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("cubic sample at t=%.3f exceeds the documented chord tolerance", testT)
		}
		if distanceToPolyline(cubicCoordinate(50, 60, 70, 80, testT), cubicCoordinate(20, 40, 40, 20, testT), path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("relative cubic sample at t=%.3f exceeds the documented chord tolerance", testT)
		}
	}
}

func TestParseFlattensSmoothCubicAndQuadraticPathCommands(t *testing.T) {
	source := []byte(`<svg width="140mm" height="100mm" viewBox="0 0 140 100"><path d="M10 80 C20 100 40 100 50 80 S80 60 90 80 Q100 100 110 80 T130 80"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	if path.Closed || len(path.Points) < 8 {
		t.Fatalf("smooth cubic and quadratic commands produced an unexpected path: %#v", path)
	}
	if first, last := path.Points[0], path.Points[len(path.Points)-1]; first != (svgimport.Point{XMM: 10, YMM: 20}) || last != (svgimport.Point{XMM: 130, YMM: 20}) {
		t.Fatalf("path endpoints = %#v and %#v, want (10,20) and (130,20)", first, last)
	}
	for sample := 0; sample <= 1000; sample++ {
		testT := float64(sample) / 1000
		if distanceToPolyline(cubicCoordinate(10, 20, 40, 50, testT), cubicCoordinate(20, 0, 0, 20, testT), path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("first cubic sample at t=%.3f exceeds the documented chord tolerance", testT)
		}
		if distanceToPolyline(cubicCoordinate(50, 60, 80, 90, testT), cubicCoordinate(20, 40, 40, 20, testT), path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("smooth cubic sample at t=%.3f exceeds the documented chord tolerance", testT)
		}
		if distanceToPolyline(90+20*testT, 20-40*testT*(1-testT), path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("first quadratic sample at t=%.3f exceeds the documented chord tolerance", testT)
		}
		if distanceToPolyline(110+20*testT, 20+40*testT*(1-testT), path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("smooth quadratic sample at t=%.3f exceeds the documented chord tolerance", testT)
		}
	}
}

func TestParseResetsSmoothControlPointsAndSupportsRelativeSmoothCommands(t *testing.T) {
	source := []byte(`<svg width="160mm" height="100mm" viewBox="0 0 160 100"><path d="M10 80 c10 20 30 20 40 0 s40 -20 40 0 L100 80 s10 0 20 0 q10 20 20 0 t20 0"/><path d="M10 60 Q20 40 30 60 L40 60 T50 60"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	if path.Closed || len(path.Points) < 8 {
		t.Fatalf("relative smooth commands produced an unexpected path: %#v", path)
	}
	if first, last := path.Points[0], path.Points[len(path.Points)-1]; first != (svgimport.Point{XMM: 10, YMM: 20}) || last != (svgimport.Point{XMM: 160, YMM: 20}) {
		t.Fatalf("path endpoints = %#v and %#v, want (10,20) and (160,20)", first, last)
	}
	// After the line, S starts with the current point (100,20) as its first
	// control rather than reflecting the earlier cubic control.
	if distanceToPolyline(105, 20, path.Points) > svgimport.CurveToleranceMM {
		t.Fatal("smooth cubic after a line did not reset its reflected control point")
	}
	resetQuadratic := got.Objects[1].Subpaths[0]
	for _, point := range resetQuadratic.Points {
		if point.XMM >= 40 && point.YMM != 40 {
			t.Fatalf("smooth quadratic after a line used a reflected control: %#v", resetQuadratic.Points)
		}
	}
}

func cubicCoordinate(start, control1, control2, end, t float64) float64 {
	oneMinusT := 1 - t
	return oneMinusT*oneMinusT*oneMinusT*start + 3*oneMinusT*oneMinusT*t*control1 + 3*oneMinusT*t*t*control2 + t*t*t*end
}

func TestParseMeasuresCurveToleranceAfterGroupTransform(t *testing.T) {
	source := []byte(`<svg width="200mm" height="200mm" viewBox="0 0 200 200"><g transform="scale(2)"><path d="M0 0 Q50 100 100 0"/></g></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	if len(path.Points) <= 59 {
		t.Fatalf("scaled curve has only %d points; flattening must account for its final physical scale", len(path.Points))
	}
	for sample := 0; sample <= 1000; sample++ {
		testT := float64(sample) / 1000
		x := 200 * testT
		y := 200 - 400*testT*(1-testT)
		if distanceToPolyline(x, y, path.Points) > svgimport.CurveToleranceMM+1e-6 {
			t.Fatalf("transformed curve sample at t=%.3f exceeds the documented chord tolerance", testT)
		}
	}
}

func TestParseRejectsCurvesThatExceedPointBudget(t *testing.T) {
	d := "M1 0 " + strings.Repeat("C0 1 2 1 1 0 ", 1000)
	source := []byte(`<svg width="2mm" height="2mm" viewBox="0 0 2 2"><path d="` + d + `"/></svg>`)
	if _, err := svgimport.Parse(source); err == nil || !strings.Contains(err.Error(), "4000-point") {
		t.Fatalf("curve point budget error = %v, want a 4000-point limit error", err)
	}
}

func distanceToPolyline(x, y float64, points []svgimport.Point) float64 {
	minimum := math.Inf(1)
	for i := 1; i < len(points); i++ {
		start, end := points[i-1], points[i]
		dx, dy := end.XMM-start.XMM, end.YMM-start.YMM
		lengthSquared := dx*dx + dy*dy
		t := 0.0
		if lengthSquared > 0 {
			t = ((x-start.XMM)*dx + (y-start.YMM)*dy) / lengthSquared
			t = math.Max(0, math.Min(1, t))
		}
		distance := math.Hypot(x-(start.XMM+t*dx), y-(start.YMM+t*dy))
		minimum = math.Min(minimum, distance)
	}
	return minimum
}

func TestParseConvertsRectangleToEditableClosedPath(t *testing.T) {
	source := []byte(`<svg width="40mm" height="20mm" viewBox="0 0 40 20"><rect x="1" y="2" width="4" height="3"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	want := []svgimport.Point{{XMM: 1, YMM: 18}, {XMM: 5, YMM: 18}, {XMM: 5, YMM: 15}, {XMM: 1, YMM: 15}}
	if !path.Closed || len(path.Points) != len(want) {
		t.Fatalf("parsed rectangle = %#v", path)
	}
	for i := range want {
		if path.Points[i] != want[i] {
			t.Fatalf("point %d = %#v, want %#v", i, path.Points[i], want[i])
		}
	}
}

func TestParseAppliesSupportedShapeTransform(t *testing.T) {
	source := []byte(`<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1" transform="translate(5,5)"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	want := []svgimport.Point{{XMM: 5, YMM: 15}, {XMM: 6, YMM: 14}}
	for i := range want {
		if path.Points[i] != want[i] {
			t.Fatalf("point %d = %#v, want %#v", i, path.Points[i], want[i])
		}
	}
}

func TestParseConvertsCircleToEditablePolyline(t *testing.T) {
	source := []byte(`<svg width="40mm" height="20mm" viewBox="0 0 40 20"><circle cx="10" cy="10" r="5"/></svg>`)
	got, err := svgimport.Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	path := got.Objects[0].Subpaths[0]
	if !path.Closed || len(path.Points) != 32 || path.Points[0] != (svgimport.Point{XMM: 15, YMM: 10}) {
		t.Fatalf("circle import = %#v", path)
	}
}

func TestParseRejectsMalformedSVGNumberListsAndCurves(t *testing.T) {
	for name, source := range map[string]string{
		"repeated comma":           `<svg width="40mm" height="20mm" viewBox="0,,0 40 20"><polyline points="0,0 1,1"/></svg>`,
		"trailing viewBox comma":   `<svg width="40mm" height="20mm" viewBox="0 0 40 20,"><polyline points="0,0 1,1"/></svg>`,
		"trailing points comma":    `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1,"/></svg>`,
		"trailing transform comma": `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1" transform="translate(1,)"/></svg>`,
		"line after close":         `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M0 0 L10 0 L10 10 Z L20 20"/></svg>`,
		"malformed smooth cubic":   `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M0 0 S2 2 4"/></svg>`,
		"malformed smooth quad":    `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M0 0 T4"/></svg>`,
		"unsupported arc":          `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M0 0 A2 2 0 0 1 4 4"/></svg>`,
		"malformed cubic path":     `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M0 0 C1 2 3 4 5"/></svg>`,
		"malformed quadratic path": `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M0 0 Q1 2 3"/></svg>`,
		"style content":            `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1" style="fill:url(https://example.invalid/a)"/></svg>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svgimport.Parse([]byte(source)); err == nil {
				t.Fatal("malformed or unsupported SVG was accepted")
			}
		})
	}
}
