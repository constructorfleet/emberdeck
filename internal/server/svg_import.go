package server

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/ConstructorFleet/emberdeck/importer"
	"github.com/ConstructorFleet/emberdeck/svgimport"
)

func (s *Server) importSVG(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "image/svg+xml" {
		fail(w, http.StatusUnsupportedMediaType, "Content-Type must be image/svg+xml")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, svgimport.MaxBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			fail(w, http.StatusRequestEntityTooLarge, "SVG exceeds the 1 MiB import limit")
			return
		}
		fail(w, http.StatusBadRequest, "could not read SVG content")
		return
	}
	registry, err := importer.NewRegistry(svgimport.Importer{})
	if err != nil {
		fail(w, http.StatusInternalServerError, "SVG importer is unavailable")
		return
	}
	result, err := registry.Import(r.Context(), "svg", importer.Request{
		SourceFormat: "svg",
		OutputType:   importer.OutputEditableDesign,
		Source:       body,
	})
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var document svgimport.Document
	if err := json.Unmarshal(result.Payload, &document); err != nil {
		fail(w, http.StatusInternalServerError, "SVG importer returned invalid editable geometry")
		return
	}
	respond(w, http.StatusOK, struct {
		svgimport.Document
		Provenance importer.Provenance `json:"provenance"`
		Warnings   []importer.Warning  `json:"warnings,omitempty"`
	}{Document: document, Provenance: result.Provenance, Warnings: result.Warnings})
}
