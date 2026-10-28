package svgimport_test

import (
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
		{"nested group", `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><g><polyline points="0,0 1,1"/></g></svg>`},
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
		"unsupported cubic path":   `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><path d="M0 0 C2 2 4 4 6 6"/></svg>`,
		"style content":            `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><polyline points="0,0 1,1" style="fill:url(https://example.invalid/a)"/></svg>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svgimport.Parse([]byte(source)); err == nil {
				t.Fatal("malformed or unsupported SVG was accepted")
			}
		})
	}
}
