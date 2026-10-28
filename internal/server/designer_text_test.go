package server

import (
	"reflect"
	"testing"
)

func TestMonolineTextGeometryIsDeterministicAndMillimetreBounded(t *testing.T) {
	text := VectorText{Content: "A1", Font: monolineFontID, SizeMM: 7, XMM: 12, YMM: 4}
	first, err := generateMonolineText(text)
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateMonolineText(text)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same text source generated different geometry")
	}
	if len(first) != 4 || first[0].Closed {
		t.Fatalf("expected deterministic open strokes for A1, got %d paths", len(first))
	}
	var minX, minY, maxX, maxY float64
	minX, minY = 1e9, 1e9
	for _, path := range first {
		if path.Closed || len(path.Points) < 2 {
			t.Fatalf("text stroke must be open and contain at least two points: %+v", path)
		}
		for _, point := range path.Points {
			if point.XMM < minX {
				minX = point.XMM
			}
			if point.YMM < minY {
				minY = point.YMM
			}
			if point.XMM > maxX {
				maxX = point.XMM
			}
			if point.YMM > maxY {
				maxY = point.YMM
			}
		}
	}
	if minX != 12 || minY != 4 || maxX != 23 || maxY != 11 {
		t.Fatalf("unexpected A1 bounds: (%v,%v)-(%v,%v)", minX, minY, maxX, maxY)
	}
	if got := first[0].Points; !reflect.DeepEqual(got, []VectorPoint{{XMM: 12, YMM: 4}, {XMM: 14.5, YMM: 11}}) {
		t.Fatalf("A left stroke differs from font definition: %+v", got)
	}
}

func TestMonolineTextRejectsUnsupportedOrUnboundedSources(t *testing.T) {
	cases := []VectorText{
		{Content: "TEXT", Font: "unknown", SizeMM: 10},
		{Content: "TEXT", Font: monolineFontID, SizeMM: 0},
		{Content: "TEXT", Font: monolineFontID, SizeMM: 101},
		{Content: "TEXT TEXT TEXT TEXT TEXT TEXT TEXT TEXT TEXT", Font: monolineFontID, SizeMM: 10},
		{Content: "café", Font: monolineFontID, SizeMM: 10},
		{Content: "K", Font: monolineFontID, SizeMM: 10},
	}
	for _, source := range cases {
		if _, err := generateMonolineText(source); err == nil {
			t.Errorf("accepted invalid monoline text source: %+v", source)
		}
	}
}

func TestMonolineQAtWorkspaceOriginHasNoNegativeCoordinates(t *testing.T) {
	paths, err := generateMonolineText(VectorText{Content: "Q", Font: monolineFontID, SizeMM: 7, XMM: 0, YMM: 0})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		for _, point := range path.Points {
			if point.XMM < 0 || point.YMM < 0 {
				t.Fatalf("Q at origin generated negative workspace coordinate: %+v", point)
			}
		}
	}
}

func TestMonolinePunctuationBearingDoesNotChangeSourceOrigin(t *testing.T) {
	source := VectorText{Content: ".", Font: monolineFontID, SizeMM: 7, XMM: 12, YMM: 4}
	paths, err := generateMonolineText(source)
	if err != nil {
		t.Fatal(err)
	}
	if source.XMM != 12 || source.YMM != 4 {
		t.Fatalf("generation mutated the editable text origin: %+v", source)
	}
	if len(paths) != 1 || paths[0].Points[0] != (VectorPoint{XMM: 14.5, YMM: 4}) {
		t.Fatalf("period bearing was not applied to geometry independently of source origin: %+v", paths)
	}
}
