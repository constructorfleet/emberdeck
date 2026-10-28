package server

import (
	"errors"
	"io"
	"mime"
	"net/http"

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
	imported, err := svgimport.Parse(body)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	respond(w, http.StatusOK, imported)
}
