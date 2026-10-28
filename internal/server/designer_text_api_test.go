package server_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

func TestMonolineTextGeometryPublicAPIAndCutRejection(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileID := createTestProfile(t, s)
	t.Run("public geometry", func(t *testing.T) {
		geometry := request(t, s, http.MethodPost, "/api/v1/designer/text-geometry",
			`{"content":"A1","font":"emberdeck-stick-v1","size_mm":7,"x_mm":12,"y_mm":4}`, "test-key")
		if geometry.Code != http.StatusOK || !strings.Contains(geometry.Body.String(), `"subpaths"`) {
			t.Fatalf("text geometry endpoint did not return stroke paths: %d %s", geometry.Code, geometry.Body.String())
		}
		var preview struct {
			Subpaths []struct {
				Points []struct {
					XMM float64 `json:"x_mm"`
					YMM float64 `json:"y_mm"`
				} `json:"points"`
				Closed bool `json:"closed"`
			} `json:"subpaths"`
		}
		if err := json.Unmarshal(geometry.Body.Bytes(), &preview); err != nil || len(preview.Subpaths) != 4 {
			t.Fatalf("decode preview paths: count=%d err=%v body=%s", len(preview.Subpaths), err, geometry.Body.String())
		}
	})
	t.Run("cut rejection", func(t *testing.T) {
		project := `{"schema_version":2,"name":"Text cut","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"text","id":"text-a","operation_id":"cut","text":{"content":"A","font":"emberdeck-stick-v1","size_mm":10,"x_mm":10,"y_mm":10},"subpaths":[]}],"operations":[{"id":"cut","type":"vector-cut","feed_mm_min":300,"power_percent":50}]}`
		w := request(t, s, http.MethodPost, "/api/v1/projects", project, "test-key")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "text objects can only be engraved") {
			t.Fatalf("text cut assignment must be rejected explicitly: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("field bounds and unsupported input", func(t *testing.T) {
		for _, input := range []string{
			`{"content":"","font":"emberdeck-stick-v1","size_mm":10,"x_mm":0,"y_mm":0}`,
			`{"content":"` + strings.Repeat("A", 33) + `","font":"emberdeck-stick-v1","size_mm":10,"x_mm":0,"y_mm":0}`,
			`{"content":"A","font":"emberdeck-stick-v1","size_mm":101,"x_mm":0,"y_mm":0}`,
			`{"content":"K","font":"emberdeck-stick-v1","size_mm":10,"x_mm":0,"y_mm":0}`,
		} {
			w := request(t, s, http.MethodPost, "/api/v1/designer/text-geometry", input, "test-key")
			if w.Code != http.StatusBadRequest {
				t.Errorf("invalid text input accepted: status=%d body=%s", w.Code, w.Body.String())
			}
		}
	})
}

func TestTextProjectPersistsEditableSourceAndGeneratesCanonicalGCode(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	profileID := createTestProfile(t, s)
	project := `{"schema_version":2,"name":"Stroke text","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"text","id":"caption","operation_id":"engrave","text":{"content":"A1","font":"emberdeck-stick-v1","size_mm":7,"x_mm":12,"y_mm":4}}],"operations":[{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}`
	w := request(t, s, http.MethodPost, "/api/v1/projects", project, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("save text project: %d %s", w.Code, w.Body.String())
	}
	var saved struct {
		ID      string `json:"id"`
		Objects []struct {
			Text struct {
				Content string  `json:"content"`
				Font    string  `json:"font"`
				SizeMM  float64 `json:"size_mm"`
			} `json:"text"`
			Subpaths []struct {
				Points []struct {
					XMM float64 `json:"x_mm"`
					YMM float64 `json:"y_mm"`
				} `json:"points"`
				Closed bool `json:"closed"`
			} `json:"subpaths"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.ID == "" || len(saved.Objects) != 1 || saved.Objects[0].Text.Content != "A1" || saved.Objects[0].Text.Font != "emberdeck-stick-v1" || saved.Objects[0].Text.SizeMM != 7 || len(saved.Objects[0].Subpaths) != 4 {
		t.Fatalf("saved text source/geometry missing: %+v err=%v body=%s", saved, err, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w = request(t, s, http.MethodGet, "/api/v1/projects/"+saved.ID, "", "test-key")
	var reopened struct {
		Objects []struct {
			Text struct {
				Content string  `json:"content"`
				Font    string  `json:"font"`
				SizeMM  float64 `json:"size_mm"`
			} `json:"text"`
			Subpaths []struct {
				Points []struct {
					XMM float64 `json:"x_mm"`
					YMM float64 `json:"y_mm"`
				} `json:"points"`
				Closed bool `json:"closed"`
			} `json:"subpaths"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil || w.Code != http.StatusOK || reopened.Objects[0].Text.Content != "A1" || !reflect.DeepEqual(saved.Objects[0].Subpaths, reopened.Objects[0].Subpaths) {
		t.Fatalf("text source/geometry did not survive reopen: %d %s err=%v", w.Code, w.Body.String(), err)
	}
	w = request(t, s, http.MethodPost, "/api/v1/projects/"+saved.ID+"/generate", `{"revision":1}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("generate text project: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, http.MethodGet, w.Header().Get("Location"), "", "test-key")
	var job struct {
		State string `json:"state"`
		Gcode string `json:"gcode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.State != "queued" || !strings.Contains(job.Gcode, "G0 X12 Y4\nM4 S0\nG1 X14.5 Y11 F600 S250") {
		t.Fatalf("text geometry did not drive queued G-code: state=%q gcode=%q err=%v", job.State, job.Gcode, err)
	}
}
