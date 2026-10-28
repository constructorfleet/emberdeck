package svgimport

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/ConstructorFleet/emberdeck/importer"
)

// Importer adapts the bounded SVG parser to the public versioned importer
// interface. It returns editable geometry without assigning machine settings.
type Importer struct{}

func (Importer) Manifest() importer.Manifest {
	return importer.Manifest{
		ProtocolVersion: importer.ProtocolVersion,
		ID:              "svg",
		Name:            "Static SVG",
		Version:         "1.0.0",
		SourceFormats: []importer.SourceFormat{{
			ID:         "svg",
			Name:       "Scalable Vector Graphics",
			Extensions: []string{".svg"},
			MediaTypes: []string{"image/svg+xml"},
		}},
		OutputTypes:   []importer.OutputType{importer.OutputEditableDesign},
		OptionsSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func (Importer) Import(ctx context.Context, request importer.Request) (importer.Result, error) {
	if ctx == nil {
		return importer.Result{}, errors.New("import context is required")
	}
	if err := ctx.Err(); err != nil {
		return importer.Result{}, err
	}
	if request.SourceFormat != "svg" || request.OutputType != importer.OutputEditableDesign {
		return importer.Result{}, errors.New("SVG importer requires SVG input and editable-design output")
	}
	if len(request.Options) != 0 {
		var options map[string]json.RawMessage
		if err := json.Unmarshal(request.Options, &options); err != nil || len(options) != 0 {
			return importer.Result{}, errors.New("SVG importer does not support conversion options")
		}
	}
	document, err := Parse(request.Source)
	if err != nil {
		return importer.Result{}, err
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return importer.Result{}, err
	}
	return importer.Result{OutputType: importer.OutputEditableDesign, Payload: payload}, nil
}
