package importer_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/importer"
	"github.com/ConstructorFleet/emberdeck/svgimport"
)

func TestSVGImporterRegistryReturnsEditableGeometryAndProvenance(t *testing.T) {
	registry, err := importer.NewRegistry(svgimport.Importer{})
	if err != nil {
		t.Fatal(err)
	}
	manifests := registry.List()
	if len(manifests) != 1 {
		t.Fatalf("registered importers = %+v", manifests)
	}
	manifest := manifests[0]
	if manifest.ProtocolVersion != importer.ProtocolVersion || manifest.ID != "svg" || manifest.Version == "" || len(manifest.SourceFormats) != 1 || len(manifest.OutputTypes) != 1 || manifest.OutputTypes[0] != importer.OutputEditableDesign {
		t.Fatalf("SVG importer manifest = %+v", manifest)
	}

	const source = `<svg xmlns="http://www.w3.org/2000/svg" width="40mm" height="20mm" viewBox="10 20 40 20"><polyline points="10,20 50,40"/></svg>`
	result, err := registry.Import(context.Background(), "svg", importer.Request{
		SourceFormat: "svg",
		OutputType:   importer.OutputEditableDesign,
		Source:       []byte(source),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputType != importer.OutputEditableDesign || result.Provenance.ImporterID != "svg" || result.Provenance.ImporterVersion != manifest.Version || result.Provenance.SourceFormat != "svg" || len(result.Provenance.Options) != 0 {
		t.Fatalf("SVG import result metadata = %+v", result)
	}
	var document svgimport.Document
	if err := json.Unmarshal(result.Payload, &document); err != nil {
		t.Fatal(err)
	}
	if document.WidthMM != 40 || document.HeightMM != 20 || len(document.Objects) != 1 || len(document.Objects[0].Subpaths) != 1 {
		t.Fatalf("SVG output is not editable geometry: %+v", document)
	}
	points := document.Objects[0].Subpaths[0].Points
	if len(points) != 2 || points[0] != (svgimport.Point{XMM: 0, YMM: 20}) || points[1] != (svgimport.Point{XMM: 40, YMM: 0}) {
		t.Fatalf("SVG output points = %+v", points)
	}
}

type fixtureImporter struct {
	manifest importer.Manifest
	calls    int
	last     importer.Request
}

func (f *fixtureImporter) Manifest() importer.Manifest { return f.manifest }

func (f *fixtureImporter) Import(_ context.Context, request importer.Request) (importer.Result, error) {
	f.calls++
	f.last = request
	return importer.Result{
		OutputType: importer.OutputGCodeJob,
		Payload:    json.RawMessage(`{"name":"converted.gcode","gcode":"M5\\n"}`),
		Warnings:   []importer.Warning{{Code: "lossy-conversion", Message: "source metadata was dropped", Lossy: true}},
	}, nil
}

func TestRegistryPinsCapabilitiesPassesOptionsAndRecordsLossyProvenance(t *testing.T) {
	fixture := &fixtureImporter{manifest: importer.Manifest{
		ProtocolVersion: importer.ProtocolVersion,
		ID:              "fixture",
		Name:            "Fixture converter",
		Version:         "2.1.0",
		SourceFormats: []importer.SourceFormat{{
			ID: "sample", Name: "Sample", Extensions: []string{".sample"}, MediaTypes: []string{"application/x-sample"},
		}},
		OutputTypes:   []importer.OutputType{importer.OutputEditableDesign, importer.OutputGCodeJob},
		OptionsSchema: json.RawMessage(`{"type":"object","properties":{"scale":{"type":"number"}}}`),
	}}
	registry, err := importer.NewRegistry(svgimport.Importer{}, fixture)
	if err != nil {
		t.Fatal(err)
	}
	manifests := registry.List()
	if len(manifests) != 2 || manifests[0].ID != "fixture" || manifests[1].ID != "svg" {
		t.Fatalf("discovered capabilities = %+v", manifests)
	}
	manifests[0].SourceFormats[0].Extensions[0] = ".mutated"
	if registry.List()[0].SourceFormats[0].Extensions[0] != ".sample" {
		t.Fatal("List exposed mutable registered capabilities")
	}

	options := json.RawMessage(`{"scale":2}`)
	result, err := registry.Import(context.Background(), "fixture", importer.Request{
		SourceFormat: "sample",
		OutputType:   importer.OutputGCodeJob,
		Source:       []byte("sample bytes"),
		Options:      options,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fixture.calls != 1 || string(fixture.last.Options) != string(options) {
		t.Fatalf("import call=%d options=%s", fixture.calls, fixture.last.Options)
	}
	if result.Provenance.ImporterID != "fixture" || result.Provenance.ImporterVersion != "2.1.0" || result.Provenance.SourceFormat != "sample" || string(result.Provenance.Options) != string(options) {
		t.Fatalf("provenance = %+v", result.Provenance)
	}
	if len(result.Warnings) != 1 || !result.Warnings[0].Lossy || result.Warnings[0].Code != "lossy-conversion" {
		t.Fatalf("lossy warning = %+v", result.Warnings)
	}

	for name, request := range map[string]importer.Request{
		"unsupported input":  {SourceFormat: "unknown", OutputType: importer.OutputGCodeJob, Source: []byte("sample")},
		"unsupported output": {SourceFormat: "sample", OutputType: "controller-settings", Source: []byte("sample")},
		"oversized source":   {SourceFormat: "sample", OutputType: importer.OutputGCodeJob, Source: []byte(strings.Repeat("x", importer.MaxSourceBytes+1))},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.Import(context.Background(), "fixture", request); err == nil {
				t.Fatal("invalid request was accepted")
			}
			if fixture.calls != 1 {
				t.Fatalf("invalid request reached importer %d times", fixture.calls-1)
			}
		})
	}
	if _, err := registry.Import(context.Background(), "missing", importer.Request{}); err == nil {
		t.Fatal("unknown importer was accepted")
	}
	if _, err := importer.NewRegistry(&fixtureImporter{manifest: importer.Manifest{ID: "bad", ProtocolVersion: importer.ProtocolVersion + 1}}); err == nil {
		t.Fatal("unsupported importer protocol was accepted")
	}
}

func TestRegistryRejectsInvalidImporterResult(t *testing.T) {
	for name, result := range map[string]importer.Result{
		"wrong output":      {OutputType: importer.OutputEditableDesign, Payload: json.RawMessage(`{}`)},
		"malformed payload": {OutputType: importer.OutputGCodeJob, Payload: json.RawMessage(`not json`)},
		"oversized payload": {OutputType: importer.OutputGCodeJob, Payload: json.RawMessage(`"` + strings.Repeat("x", importer.MaxOutputBytes) + `"`)},
		"invalid warning":   {OutputType: importer.OutputGCodeJob, Payload: json.RawMessage(`{}`), Warnings: []importer.Warning{{Code: "", Message: "bad"}}},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := &staticImporter{result: result}
			registry, err := importer.NewRegistry(fixture)
			if err != nil {
				t.Fatal(err)
			}
			_, err = registry.Import(context.Background(), "fixture", importer.Request{SourceFormat: "sample", OutputType: importer.OutputGCodeJob, Source: []byte("sample")})
			if err == nil {
				t.Fatal("invalid importer result was accepted")
			}
		})
	}
}

type staticImporter struct{ result importer.Result }

func (s *staticImporter) Manifest() importer.Manifest {
	return importer.Manifest{
		ProtocolVersion: importer.ProtocolVersion,
		ID:              "fixture", Name: "Fixture", Version: "1.0.0",
		SourceFormats: []importer.SourceFormat{{ID: "sample", Name: "Sample", Extensions: []string{".sample"}, MediaTypes: []string{"application/x-sample"}}},
		OutputTypes:   []importer.OutputType{importer.OutputGCodeJob},
	}
}

func (s *staticImporter) Import(context.Context, importer.Request) (importer.Result, error) {
	return s.result, nil
}

func TestRegistryStopsBeforeCallingImporterWhenContextIsCanceled(t *testing.T) {
	fixture := &fixtureImporter{manifest: importer.Manifest{
		ProtocolVersion: importer.ProtocolVersion,
		ID:              "fixture", Name: "Fixture", Version: "1.0.0",
		SourceFormats: []importer.SourceFormat{{ID: "sample", Name: "Sample", Extensions: []string{".sample"}, MediaTypes: []string{"application/x-sample"}}},
		OutputTypes:   []importer.OutputType{importer.OutputGCodeJob},
	}}
	registry, err := importer.NewRegistry(fixture)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = registry.Import(ctx, "fixture", importer.Request{SourceFormat: "sample", OutputType: importer.OutputGCodeJob, Source: []byte("sample")})
	if !errors.Is(err, context.Canceled) || fixture.calls != 0 {
		t.Fatalf("canceled import error=%v calls=%d", err, fixture.calls)
	}
}
