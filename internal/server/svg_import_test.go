package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

func TestSVGImportReturnsEditableGeometryWithoutMachineSettings(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	request := httptest.NewRequest(http.MethodPost, "/api/v1/svg-import", strings.NewReader(`<svg xmlns="http://www.w3.org/2000/svg" width="40mm" height="20mm" viewBox="10 20 40 20"><polyline points="10,20 50,40"/></svg>`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "image/svg+xml")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("SVG import = %d %s", response.Code, response.Body.String())
	}
	var imported struct {
		WidthMM    float64 `json:"width_mm"`
		HeightMM   float64 `json:"height_mm"`
		Provenance struct {
			ImporterID      string `json:"importer_id"`
			ImporterVersion string `json:"importer_version"`
			SourceFormat    string `json:"source_format"`
		} `json:"provenance"`
		Objects []struct {
			Subpaths []struct {
				Points []struct {
					X float64 `json:"x_mm"`
					Y float64 `json:"y_mm"`
				} `json:"points"`
				Closed bool `json:"closed"`
			} `json:"subpaths"`
			OperationID string `json:"operation_id"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if imported.WidthMM != 40 || imported.HeightMM != 20 || imported.Provenance.ImporterID != "svg" || imported.Provenance.ImporterVersion != "1.0.0" || imported.Provenance.SourceFormat != "svg" || len(imported.Objects) != 1 || imported.Objects[0].OperationID != "" {
		t.Fatalf("imported SVG summary = %+v", imported)
	}
	points := imported.Objects[0].Subpaths[0].Points
	if len(points) != 2 || points[0].X != 0 || points[0].Y != 20 || points[1].X != 40 || points[1].Y != 0 || imported.Objects[0].Subpaths[0].Closed {
		t.Fatalf("imported SVG path = %+v", imported.Objects[0])
	}
}

func TestSVGImportAPIComposesNestedGroupsAndReturnsUnassignedGeometry(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	source := `<svg width="100mm" height="100mm" viewBox="0 0 100 100"><g transform="translate(10 20)"><g transform="scale(2)"><line x1="1" y1="2" x2="3" y2="4"/></g></g></svg>`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/svg-import", strings.NewReader(source))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "image/svg+xml")
	response := httptest.NewRecorder()
	s.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("nested SVG import = %d %s", response.Code, response.Body.String())
	}
	var imported struct {
		Objects []struct {
			Subpaths []struct {
				Points []struct {
					X float64 `json:"x_mm"`
					Y float64 `json:"y_mm"`
				} `json:"points"`
			} `json:"subpaths"`
			OperationID string `json:"operation_id"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Objects) != 1 || imported.Objects[0].OperationID != "" {
		t.Fatalf("nested SVG objects should remain unassigned: %+v", imported.Objects)
	}
	points := imported.Objects[0].Subpaths[0].Points
	want := []struct{ X, Y float64 }{{12, 76}, {16, 72}}
	if len(points) != len(want) {
		t.Fatalf("nested API points = %+v, want %+v", points, want)
	}
	for i := range want {
		if points[i].X != want[i].X || points[i].Y != want[i].Y {
			t.Fatalf("point %d = %+v, want %+v", i, points[i], want[i])
		}
	}

	unsupported := httptest.NewRequest(http.MethodPost, "/api/v1/svg-import", strings.NewReader(`<svg width="40mm" height="20mm" viewBox="0 0 40 20"><g style="fill:red"><line x1="0" y1="0" x2="1" y2="1"/></g></svg>`))
	unsupported.Header.Set("Authorization", "Bearer test-key")
	unsupported.Header.Set("Content-Type", "image/svg+xml")
	unsupportedResponse := httptest.NewRecorder()
	s.ServeHTTP(unsupportedResponse, unsupported)
	if unsupportedResponse.Code != http.StatusUnprocessableEntity {
		t.Fatalf("SVG import with unsupported group styling = %d %s", unsupportedResponse.Code, unsupportedResponse.Body.String())
	}
}

func TestSVGImportAPIConvertsMonolineTextAndRejectsUnsupportedText(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	request := func(source string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/svg-import", strings.NewReader(source))
		r.Header.Set("Authorization", "Bearer test-key")
		r.Header.Set("Content-Type", "image/svg+xml")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	source := `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><text x="2" y="10" font-family="emberdeck-stick-v1" font-size="7">A</text></svg>`
	response := request(source)
	if response.Code != http.StatusOK {
		t.Fatalf("monoline SVG text import = %d %s", response.Code, response.Body.String())
	}
	var imported struct {
		Objects []struct {
			Subpaths []struct {
				Points []struct {
					X float64 `json:"x_mm"`
					Y float64 `json:"y_mm"`
				} `json:"points"`
				Closed bool `json:"closed"`
			} `json:"subpaths"`
			OperationID string `json:"operation_id"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &imported); err != nil {
		t.Fatal(err)
	}
	if len(imported.Objects) != 1 || len(imported.Objects[0].Subpaths) != 3 || imported.Objects[0].OperationID != "" {
		t.Fatalf("monoline text should become unassigned editable geometry: %+v", imported.Objects)
	}
	first := imported.Objects[0].Subpaths[0]
	if first.Closed || len(first.Points) != 2 || first.Points[0].X != 2 || first.Points[0].Y != 10 || first.Points[1].X != 4.5 || first.Points[1].Y != 17 {
		t.Fatalf("first imported text stroke = %+v", first)
	}
	unsupported := request(`<svg width="40mm" height="20mm" viewBox="0 0 40 20"><text font-family="emberdeck-stick-v1" font-size="7" style="fill:red">A</text></svg>`)
	if unsupported.Code != http.StatusUnprocessableEntity {
		t.Fatalf("styled SVG text = %d %s, want unsupported content rejection", unsupported.Code, unsupported.Body.String())
	}
}

func TestSVGImportRequiresImageContentTypeAndRejectsActiveContent(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	request := func(contentType, source string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/svg-import", bytes.NewBufferString(source))
		r.Header.Set("Authorization", "Bearer test-key")
		r.Header.Set("Content-Type", contentType)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if got := request("application/json", `<svg/>`); got.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("wrong content type = %d %s", got.Code, got.Body.String())
	}
	if got := request("image/svg+xml", `<svg width="40mm" height="20mm" viewBox="0 0 40 20"><script>alert(1)</script></svg>`); got.Code != http.StatusUnprocessableEntity {
		t.Fatalf("active SVG content = %d %s", got.Code, got.Body.String())
	}
}

func TestSVGImportRequiresAuthenticationAndEnforcesSizeLimit(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	unauthorized := httptest.NewRequest(http.MethodPost, "/api/v1/svg-import", strings.NewReader(`<svg/>`))
	unauthorized.Header.Set("Content-Type", "image/svg+xml")
	unauthorizedResponse := httptest.NewRecorder()
	s.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated SVG import = %d", unauthorizedResponse.Code)
	}

	tooLarge := httptest.NewRequest(http.MethodPost, "/api/v1/svg-import", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	tooLarge.Header.Set("Authorization", "Bearer test-key")
	tooLarge.Header.Set("Content-Type", "image/svg+xml")
	tooLargeResponse := httptest.NewRecorder()
	s.ServeHTTP(tooLargeResponse, tooLarge)
	if tooLargeResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized SVG import = %d %s", tooLargeResponse.Code, tooLargeResponse.Body.String())
	}
}
