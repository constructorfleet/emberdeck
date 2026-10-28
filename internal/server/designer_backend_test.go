package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/internal/server"
)

const profileBody = `{"schema_version":1,"name":"Declared GRBL","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`

func TestDeclaredMachineProfileCRUDPersistsWithOptimisticRevision(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "POST", "/api/v1/profiles", profileBody, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("create profile: %d %s", w.Code, w.Body.String())
	}
	var profile struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
		Name     string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if len(profile.ID) != 32 || profile.Revision != 1 || profile.Name != "Declared GRBL" {
		t.Fatalf("unexpected profile: %+v", profile)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w = request(t, s, "GET", "/api/v1/profiles/"+profile.ID, "", "test-key")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"name":"Declared GRBL"`) {
		t.Fatalf("profile did not survive reopen: %d %s", w.Code, w.Body.String())
	}
	updated := `{"schema_version":1,"name":"Declared GRBL v2","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true,"revision":1}`
	w = request(t, s, "PUT", "/api/v1/profiles/"+profile.ID, updated, "test-key")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"revision":2`) {
		t.Fatalf("update profile: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "PUT", "/api/v1/profiles/"+profile.ID, updated, "test-key")
	if w.Code != http.StatusConflict {
		t.Fatalf("stale revision returned %d: %s", w.Code, w.Body.String())
	}
	w = request(t, s, "DELETE", "/api/v1/profiles/"+profile.ID, `{"revision":2}`, "test-key")
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete profile: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/profiles/"+profile.ID, "", "test-key")
	if w.Code != http.StatusNotFound {
		t.Fatalf("deleted profile returned %d", w.Code)
	}
}

func TestProfileWritesRequireAuthStrictSchemaAndDeclaredGRBLLimits(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if w := request(t, s, "POST", "/api/v1/profiles", profileBody, "bad"); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create returned %d", w.Code)
	}
	for _, body := range []string{
		`{}`, profileBody + ` {}`,
		strings.TrimSuffix(profileBody, `}`) + `,"revision":0}`,
		strings.TrimSuffix(profileBody, `}`) + `,"id":null}`,
		`{"id":"spoofed","schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`,
		`{"schema_version":1,"name":"x","controller":"ruida","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`,
		`{"schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300.0001,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`,
		`{"schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":false}`,
		`{"schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":1,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`,
	} {
		if w := request(t, s, "POST", "/api/v1/profiles", body, "test-key"); w.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid profile (%s): %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := request(t, s, "GET", "/api/v1/profiles", "", "test-key"); w.Code != http.StatusOK || w.Body.String() != "[]\n" {
		t.Fatalf("invalid profile was stored: %d %s", w.Code, w.Body.String())
	}
}

func TestProfileValidationReportsFieldLevelFeedRangeError(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	body := strings.Replace(profileBody, `"min_feed_mm_min":1`, `"min_feed_mm_min":5000`, 1)
	body = strings.Replace(body, `"max_feed_mm_min":6000`, `"max_feed_mm_min":100`, 1)
	w := request(t, s, "POST", "/api/v1/profiles", body, "test-key")
	var response struct {
		Error  string            `json:"error"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusBadRequest {
		t.Fatalf("invalid feed range response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if response.Fields["max_feed_mm_min"] != "Maximum feed must be at least the minimum feed." {
		t.Fatalf("field errors = %#v; want a maximum-feed message", response.Fields)
	}
}

func TestProjectValidationReportsFieldLevelPositionError(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileID := createTestProfile(t, s)
	body := `{"schema_version":1,"name":"Negative origin","profile_id":"` + profileID + `","profile_revision":1,"rectangle":{"x_mm":-1,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`
	w := request(t, s, "POST", "/api/v1/projects", body, "test-key")
	var response struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusBadRequest {
		t.Fatalf("invalid project response: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if response.Fields["x_mm"] != "X position cannot be negative." {
		t.Fatalf("field errors = %#v; want a position message", response.Fields)
	}
}

func TestProjectValidationReportsSpecificFieldMessages(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/profiles", profileBody, "test-key")
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create profile: %d %s %v", w.Code, w.Body.String(), err)
	}
	project := func(rectangle, operation string) string {
		return `{"schema_version":1,"name":"panel","profile_id":"` + profile.ID + `","profile_revision":1,"rectangle":` + rectangle + `,"operation":` + operation + `}`
	}
	operationJSON := `{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}`
	cases := []struct {
		name   string
		body   string
		field  string
		detail string
	}{
		{"negative x", project(`{"x_mm":-1,"y_mm":1,"width_mm":10,"height_mm":10}`, operationJSON), "x_mm", "X position cannot be negative."},
		{"excess precision", project(`{"x_mm":1.23456,"y_mm":1,"width_mm":10,"height_mm":10}`, operationJSON), "x_mm", "Use at most three decimal places."},
		{"zero width", project(`{"x_mm":1,"y_mm":1,"width_mm":0,"height_mm":10}`, operationJSON), "width_mm", "Width must be greater than zero."},
		{"outside workspace", project(`{"x_mm":295,"y_mm":1,"width_mm":10,"height_mm":10}`, operationJSON), "width_mm", "X position plus width exceeds the declared 300 mm workspace."},
		{"feed outside profile", project(`{"x_mm":1,"y_mm":1,"width_mm":10,"height_mm":10}`, `{"type":"vector-engrave","feed_mm_min":7000,"power_percent":25}`), "feed_mm_min", "Feed must be from 1 to 6000 mm/min."},
		{"power above profile", project(`{"x_mm":1,"y_mm":1,"width_mm":10,"height_mm":10}`, `{"type":"vector-engrave","feed_mm_min":600,"power_percent":150}`), "power_percent", "Commanded power must be greater than zero and at most 100%."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s, "POST", "/api/v1/projects", tc.body, "test-key")
			var response struct {
				Error  string            `json:"error"`
				Fields map[string]string `json:"fields"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusBadRequest {
				t.Fatalf("project validation response: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if response.Fields[tc.field] != tc.detail {
				t.Fatalf("field %s = %q; want %q (all fields %#v)", tc.field, response.Fields[tc.field], tc.detail, response.Fields)
			}
		})
	}
}

func TestRectangleBoundaryUsesMillimetrePrecisionWithoutFloatDrift(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileJSON := strings.Replace(profileBody, `"width_mm":300`, `"width_mm":0.3`, 1)
	w := request(t, s, "POST", "/api/v1/profiles", profileJSON, "test-key")
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create narrow profile: %d %s %v", w.Code, w.Body.String(), err)
	}
	project := `{"schema_version":1,"name":"edge","profile_id":"` + profile.ID + `","profile_revision":1,"rectangle":{"x_mm":0.1,"y_mm":1,"width_mm":0.2,"height_mm":2},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`
	w = request(t, s, "POST", "/api/v1/projects", project, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("rejected exact work-area edge: %d %s", w.Code, w.Body.String())
	}
}

func TestProjectRevisionsExposeSaveTimeAndStaleWritesReturnCurrentRevision(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileID := createTestProfile(t, s)
	body := `{"schema_version":1,"name":"Shared panel","profile_id":"` + profileID + `","profile_revision":1,"rectangle":{"x_mm":10,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`
	w := request(t, s, "POST", "/api/v1/projects", body, "test-key")
	var created struct {
		ID        string    `json:"id"`
		Revision  int       `json:"revision"`
		UpdatedAt time.Time `json:"updated_at"`
		UpdatedBy string    `json:"updated_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s %v", w.Code, w.Body.String(), err)
	}
	if created.Revision != 1 || created.UpdatedAt.IsZero() || created.UpdatedBy == "" {
		t.Fatalf("created project lacks revision metadata: %+v", created)
	}
	updatedBody := strings.TrimSuffix(body, "}") + `,"revision":1}`
	w = request(t, s, "PUT", "/api/v1/projects/"+created.ID, updatedBody, "test-key")
	var updated struct {
		Revision  int       `json:"revision"`
		UpdatedAt time.Time `json:"updated_at"`
		UpdatedBy string    `json:"updated_by"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil || w.Code != http.StatusOK {
		t.Fatalf("update project: %d %s %v", w.Code, w.Body.String(), err)
	}
	if updated.Revision != 2 || updated.UpdatedAt.IsZero() || updated.UpdatedAt.Before(created.UpdatedAt) || updated.UpdatedBy == "" {
		t.Fatalf("updated metadata did not advance: created=%+v updated=%+v", created, updated)
	}
	w = request(t, s, "PUT", "/api/v1/projects/"+created.ID, updatedBody, "test-key")
	var conflict struct {
		Error   string `json:"error"`
		Current struct {
			Revision  int       `json:"revision"`
			UpdatedAt time.Time `json:"updated_at"`
			UpdatedBy string    `json:"updated_by"`
		} `json:"current"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &conflict); err != nil || w.Code != http.StatusConflict {
		t.Fatalf("stale update: %d %s %v", w.Code, w.Body.String(), err)
	}
	if conflict.Error != "project revision conflict" || conflict.Current.Revision != 2 || conflict.Current.UpdatedAt.IsZero() || conflict.Current.UpdatedBy == "" {
		t.Fatalf("stale write omitted current revision: %+v", conflict)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "GET", "/api/v1/projects/"+created.ID, "", "test-key")
	var reopened server.DesignerProject
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil || w.Code != http.StatusOK {
		t.Fatalf("reopen project: %d %s %v", w.Code, w.Body.String(), err)
	}
	if reopened.Revision != updated.Revision || reopened.UpdatedAt == nil || !reopened.UpdatedAt.Equal(updated.UpdatedAt) || reopened.UpdatedBy != updated.UpdatedBy {
		t.Fatalf("save metadata did not persist: %+v", reopened)
	}
}

func TestGenerateRoundsExactHalfPowerUp(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileJSON := strings.Replace(profileBody, `"max_s":1000`, `"max_s":50000`, 1)
	w := request(t, s, "POST", "/api/v1/profiles", profileJSON, "test-key")
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create profile: %d %s %v", w.Code, w.Body.String(), err)
	}
	project := `{"schema_version":1,"name":"half power","profile_id":"` + profile.ID + `","profile_revision":1,"rectangle":{"x_mm":0,"y_mm":0,"width_mm":10,"height_mm":10},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":0.009}}`
	w = request(t, s, "POST", "/api/v1/projects", project, "test-key")
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = request(t, s, "POST", "/api/v1/projects/"+created.ID+"/generate", `{"revision":1}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", w.Header().Get("Location"), "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `F600 S5`) {
		t.Fatalf("half-up power should emit S5: %d %s", w.Code, w.Body.String())
	}
}

func TestVectorProjectCreatesOrderedCutAndEngraveJob(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	profileID := createTestProfile(t, s)
	project := `{"schema_version":2,"name":"Mixed paths","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"rectangle","id":"engrave-panel","operation_id":"engrave","subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":20,"y_mm":10},{"x_mm":20,"y_mm":20},{"x_mm":10,"y_mm":20}],"closed":true}]},{"kind":"rectangle","id":"cut-panel","operation_id":"cut","subpaths":[{"points":[{"x_mm":30,"y_mm":10},{"x_mm":40,"y_mm":10},{"x_mm":40,"y_mm":20},{"x_mm":30,"y_mm":20}],"closed":true}]}],"operations":[{"id":"cut","type":"vector-cut","feed_mm_min":300,"power_percent":80},{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}`
	w := request(t, s, "POST", "/api/v1/projects", project, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("create vector project: %d %s", w.Code, w.Body.String())
	}
	var saved struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.Revision != 1 {
		t.Fatalf("decode saved vector project: %+v %v", saved, err)
	}
	w = request(t, s, "POST", "/api/v1/projects/"+saved.ID+"/generate", `{"revision":1}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("generate vector project: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"gcode"`) {
		t.Fatalf("creation response must not echo program bytes: %s", w.Body.String())
	}
	// Program bytes are served only by the single-job endpoint.
	w = request(t, s, "GET", w.Header().Get("Location"), "", "test-key")
	var job struct {
		State      string `json:"state"`
		Gcode      string `json:"gcode"`
		Provenance struct {
			GeneratorVersion string `json:"generator_version"`
			Project          struct {
				SchemaVersion int `json:"schema_version"`
				Objects       []struct {
					ID string `json:"id"`
				} `json:"objects"`
				Operations []struct {
					ID string `json:"id"`
				} `json:"operations"`
			} `json:"project"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	const expected = "M5\nG21\nG90\nG94\nG0 X30 Y10\nM4 S0\nG1 X40 Y10 F300 S800\nG1 X40 Y20\nG1 X30 Y20\nG1 X30 Y10\nM5\nG0 X10 Y10\nM4 S0\nG1 X20 Y10 F600 S250\nG1 X20 Y20\nG1 X10 Y20\nG1 X10 Y10\nM5\n"
	if job.State != "queued" || job.Gcode != expected {
		t.Fatalf("unexpected queued vector job: state=%q gcode=%q", job.State, job.Gcode)
	}
	if job.Provenance.GeneratorVersion != "vector-v1" || job.Provenance.Project.SchemaVersion != 2 || len(job.Provenance.Project.Objects) != 2 || len(job.Provenance.Project.Operations) != 2 {
		t.Fatalf("vector job provenance is incomplete: %+v", job.Provenance)
	}
	if _, err := driver.Validate(job.Gcode); err != nil {
		t.Fatalf("generated vector job is not valid GRBL input: %v", err)
	}
}

func TestVectorProjectRejectsOpenCutAndOutOfEnvelopePoints(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	profileID := createTestProfile(t, s)
	for _, project := range []string{
		`{"schema_version":2,"name":"Open cut","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"path","id":"path","operation_id":"cut","subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":20,"y_mm":10},{"x_mm":20,"y_mm":20}],"closed":false}]}],"operations":[{"id":"cut","type":"vector-cut","feed_mm_min":300,"power_percent":80}]}`,
		`{"schema_version":2,"name":"Outside","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"path","id":"path","operation_id":"engrave","subpaths":[{"points":[{"x_mm":301,"y_mm":10},{"x_mm":20,"y_mm":10}],"closed":false}]}],"operations":[{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}`,
		`{"schema_version":2,"name":"Degenerate cut","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"path","id":"path","operation_id":"cut","subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":20,"y_mm":10},{"x_mm":30,"y_mm":10}],"closed":true}]}],"operations":[{"id":"cut","type":"vector-cut","feed_mm_min":300,"power_percent":80}]}`,
		`{"schema_version":2,"name":"Unassigned outside","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"path","id":"path","subpaths":[{"points":[{"x_mm":301,"y_mm":10},{"x_mm":302,"y_mm":10}],"closed":false}]}],"operations":[]}`,
	} {
		if w := request(t, s, "POST", "/api/v1/projects", project, "test-key"); w.Code != http.StatusBadRequest {
			t.Errorf("accepted unsafe vector geometry: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestVectorProjectTransformIsSavedValidatedAndUsedForGeneration(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileID := createTestProfile(t, s)
	project := `{"schema_version":2,"name":"Transformed rectangle","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"rectangle","id":"panel","operation_id":"engrave","transform":{"translate_x_mm":5,"translate_y_mm":5,"scale_x":2,"scale_y":1,"rotation_deg":90},"subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":20,"y_mm":10},{"x_mm":20,"y_mm":20},{"x_mm":10,"y_mm":20}],"closed":true}]}],"operations":[{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}`
	w := request(t, s, "POST", "/api/v1/projects", project, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("create transformed vector project: %d %s", w.Code, w.Body.String())
	}
	var saved struct {
		ID      string `json:"id"`
		Objects []struct {
			Transform struct {
				TranslateX float64 `json:"translate_x_mm"`
				TranslateY float64 `json:"translate_y_mm"`
				ScaleX     float64 `json:"scale_x"`
				ScaleY     float64 `json:"scale_y"`
				Rotation   float64 `json:"rotation_deg"`
			} `json:"transform"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Objects) != 1 || saved.Objects[0].Transform.TranslateX != 5 || saved.Objects[0].Transform.TranslateY != 5 || saved.Objects[0].Transform.ScaleX != 2 || saved.Objects[0].Transform.ScaleY != 1 || saved.Objects[0].Transform.Rotation != 90 {
		t.Fatalf("transform was not persisted: %+v", saved)
	}
	w = request(t, s, "GET", "/api/v1/projects/"+saved.ID, "", "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("get saved transformed project: %d %s", w.Code, w.Body.String())
	}
	var reopened struct {
		Objects []struct {
			Transform struct {
				ScaleX   float64 `json:"scale_x"`
				Rotation float64 `json:"rotation_deg"`
			} `json:"transform"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil || len(reopened.Objects) != 1 || reopened.Objects[0].Transform.ScaleX != 2 || reopened.Objects[0].Transform.Rotation != 90 {
		t.Fatalf("saved transform did not survive project read: %+v err=%v body=%s", reopened, err, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/projects/"+saved.ID+"/generate", `{"revision":1}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("generate transformed vector project: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", w.Header().Get("Location"), "", "test-key")
	var job struct {
		Gcode string `json:"gcode"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	const expected = "M5\nG21\nG90\nG94\nG0 X25 Y10\nM4 S0\nG1 X25 Y30 F600 S250\nG1 X15 Y30\nG1 X15 Y10\nG1 X25 Y10\nM5\n"
	if job.Gcode != expected {
		t.Fatalf("transform was not applied to generated toolpath: got %q want %q", job.Gcode, expected)
	}
}

func TestVectorProjectRejectsInvalidOrOutOfWorkspaceTransforms(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileID := createTestProfile(t, s)
	base := `{"schema_version":2,"name":"Invalid transform","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"line","id":"line","operation_id":"engrave","transform":%s,"subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":20,"y_mm":20}],"closed":false}]}],"operations":[{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}`
	for _, transform := range []string{
		`{"scale_x":0,"scale_y":1}`,
		`{"scale_x":101,"scale_y":1}`,
		`{"scale_x":1,"scale_y":1,"rotation_deg":90.0001}`,
		`{"translate_x_mm":290,"translate_y_mm":0,"scale_x":2,"scale_y":1}`,
	} {
		body := fmt.Sprintf(base, transform)
		if w := request(t, s, "POST", "/api/v1/projects", body, "test-key"); w.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid transform %s: %d %s", transform, w.Code, w.Body.String())
		}
	}
}

func TestVectorProjectCanSaveUnassignedGeometryButCannotGenerateIt(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	profileID := createTestProfile(t, s)
	project := `{"schema_version":2,"name":"Unassigned import source","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"path","id":"path","subpaths":[{"points":[{"x_mm":10,"y_mm":10},{"x_mm":20,"y_mm":10}],"closed":false}]}],"operations":[]}`
	w := request(t, s, "POST", "/api/v1/projects", project, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("save unassigned design source: %d %s", w.Code, w.Body.String())
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "POST", "/api/v1/projects/"+saved.ID+"/generate", `{"revision":1}`, "test-key"); w.Code != http.StatusConflict {
		t.Fatalf("generated design with implicit operation: %d %s", w.Code, w.Body.String())
	}
}

func TestSavedVectorProjectRoundTripsAcrossServerRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	profileID := createTestProfile(t, s)
	project := `{"schema_version":2,"name":"Saved vectors","units":"mm","profile_id":"` + profileID + `","profile_revision":1,"objects":[{"kind":"line","id":"line-a","operation_id":"engrave","subpaths":[{"points":[{"x_mm":1.125,"y_mm":2},{"x_mm":4,"y_mm":5}],"closed":false}]}],"operations":[{"id":"engrave","type":"vector-engrave","feed_mm_min":600,"power_percent":25}]}`
	w := request(t, s, "POST", "/api/v1/projects", project, "test-key")
	var saved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create vector source: %d %s %v", w.Code, w.Body.String(), err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	w = request(t, s, "GET", "/api/v1/projects/"+saved.ID, "", "test-key")
	var reopened server.DesignerProject
	if err := json.Unmarshal(w.Body.Bytes(), &reopened); err != nil || w.Code != http.StatusOK {
		t.Fatalf("read reopened project: %d %s %v", w.Code, w.Body.String(), err)
	}
	if reopened.SchemaVersion != 2 || reopened.Units != "mm" || len(reopened.Objects) != 1 || reopened.Objects[0].Subpaths[0].Points[0] != (server.VectorPoint{XMM: 1.125, YMM: 2}) || reopened.Operations[0].ID != "engrave" {
		t.Fatalf("vector source changed after restart: %+v", reopened)
	}
}

func TestProfileRequiresExplicitZeroMinimumPower(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	body := `{"schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`
	w := request(t, s, "POST", "/api/v1/profiles", body, "test-key")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("profile without min_s was accepted: %d %s", w.Code, w.Body.String())
	}
}

func TestOpenRejectsSemanticallyInvalidPersistedMachineProfile(t *testing.T) {
	dir := t.TempDir()
	data := `{"00000000000000000000000000000000":{"schema_version":1,"id":"00000000000000000000000000000000","revision":1,"name":"Invalid loaded profile","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":1,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}}`
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := server.Open(dir, "test-key"); err == nil {
		_ = s.Close()
		t.Fatal("server opened semantically invalid profile data")
	}
	withoutMin := strings.Replace(data, `,"min_s":1`, "", 1)
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), []byte(withoutMin), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := server.Open(dir, "test-key"); err == nil {
		_ = s.Close()
		t.Fatal("server opened profile data without explicit min_s")
	}
}

func TestOpenRejectsProjectWithMissingProfileReference(t *testing.T) {
	dir := t.TempDir()
	profile := server.MachineProfile{SchemaVersion: 1, ID: "00000000000000000000000000000000", Revision: 1, Name: "Declared", Controller: "grbl-1.1", Units: "mm", Origin: "lower-left", WidthMM: 300, HeightMM: 200, MinS: 0, MaxS: 1000, MinFeed: 1, MaxFeed: 6000, MaxPowerPct: 100, LaserMode: true}
	project := server.DesignerProject{SchemaVersion: 1, ID: "11111111111111111111111111111111", Revision: 1, Name: "Broken reference", ProfileID: "ffffffffffffffffffffffffffffffff", ProfileRevision: 1,
		Rectangle: server.Rectangle{XMM: 0, YMM: 0, WidthMM: 10, HeightMM: 10}, Operation: server.DesignerOperation{Type: "vector-engrave", FeedMMMin: 600, PowerPercent: 25}}
	profiles, _ := json.Marshal(map[string]server.MachineProfile{profile.ID: profile})
	projects, _ := json.Marshal(map[string]server.DesignerProject{project.ID: project})
	if err := os.WriteFile(filepath.Join(dir, "profiles.json"), profiles, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "designs.json"), projects, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := server.Open(dir, "test-key"); err == nil {
		_ = s.Close()
		t.Fatal("server opened project with missing profile reference")
	}
}

func TestEditableRectangleProjectPersistsWithProfileRevisionAndStaleDetection(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	profileID := createTestProfile(t, s)
	projectBody := `{"schema_version":1,"name":"Panel outline","profile_id":"` + profileID + `","profile_revision":1,"rectangle":{"x_mm":10,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`
	w := request(t, s, "POST", "/api/v1/projects", projectBody, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	var project struct {
		ID           string `json:"id"`
		Revision     int    `json:"revision"`
		ProfileStale bool   `json:"profile_stale"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if len(project.ID) != 32 || project.Revision != 1 || project.ProfileStale {
		t.Fatalf("unexpected project: %+v", project)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w = request(t, s, "GET", "/api/v1/projects/"+project.ID, "", "test-key")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"width_mm":30`) {
		t.Fatalf("project did not survive reopen: %d %s", w.Code, w.Body.String())
	}
	updated := `{"schema_version":1,"name":"Panel outline v2","profile_id":"` + profileID + `","profile_revision":1,"rectangle":{"x_mm":10,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25},"revision":1}`
	w = request(t, s, "PUT", "/api/v1/projects/"+project.ID, updated, "test-key")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"revision":2`) {
		t.Fatalf("update project: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "PUT", "/api/v1/projects/"+project.ID, updated, "test-key")
	if w.Code != http.StatusConflict {
		t.Fatalf("stale project update returned %d: %s", w.Code, w.Body.String())
	}
	profileUpdate := `{"schema_version":1,"name":"Declared GRBL v2","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":20,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true,"revision":1}`
	w = request(t, s, "PUT", "/api/v1/profiles/"+profileID, profileUpdate, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("update referenced profile: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatalf("valid stale project should survive reopen: %v", err)
	}
	w = request(t, s, "DELETE", "/api/v1/profiles/"+profileID, `{"revision":2}`, "test-key")
	if w.Code != http.StatusConflict {
		t.Fatalf("deleted referenced profile: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/projects/"+project.ID, "", "test-key")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"profile_stale":true`) {
		t.Fatalf("project did not report stale profile: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/projects/"+project.ID+"/generate", `{"revision":2}`, "test-key")
	if w.Code != http.StatusConflict {
		t.Fatalf("generated from stale profile revision: %d %s", w.Code, w.Body.String())
	}
}

func TestProjectRejectsOutOfBoundsAndStaleProfileWrites(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	profileID := createTestProfile(t, s)
	body := func(profileRevision int, x float64) string {
		return fmt.Sprintf(`{"schema_version":1,"name":"bad","profile_id":"%s","profile_revision":%d,"rectangle":{"x_mm":%g,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`, profileID, profileRevision, x)
	}
	for _, tc := range []struct {
		revision int
		x        float64
		want     int
	}{{1, 271, 400}, {0, 10, 409}, {2, 10, 409}} {
		w := request(t, s, "POST", "/api/v1/projects", body(tc.revision, tc.x), "test-key")
		if w.Code != tc.want {
			t.Errorf("project write rev=%d x=%v returned %d, want %d: %s", tc.revision, tc.x, w.Code, tc.want, w.Body.String())
		}
	}
	valid := body(1, 10)
	for _, invalid := range []string{
		strings.TrimSuffix(valid, "}") + `,"unexpected":true}`,
		strings.TrimSuffix(valid, "}") + `,"revision":0}`,
		strings.TrimSuffix(valid, "}") + `,"revision":null}`,
		strings.Replace(valid, `"width_mm":30`, `"width_mm":30.0001`, 1),
		strings.Replace(valid, `"type":"vector-engrave"`, `"type":"vector-cut"`, 1),
		strings.Replace(valid, `"power_percent":25`, `"power_percent":0.001`, 1),
	} {
		w := request(t, s, "POST", "/api/v1/projects", invalid, "test-key")
		if w.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid project %s: %d %s", invalid, w.Code, w.Body.String())
		}
	}
	if w := request(t, s, "GET", "/api/v1/projects", "", "wrong-key"); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated projects list returned %d", w.Code)
	}
}

func TestGenerateCreatesValidatedQueuedJobWithImmutableSourceSnapshots(t *testing.T) {
	const expectedProgram = "M5\nG21\nG90\nG94\nG0 X10 Y20\nM4 S0\nG1 X40 Y20 F600 S250\nG1 X40 Y40\nG1 X10 Y40\nG1 X10 Y20\nM5\n"
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	profileID := createTestProfile(t, s)
	projectBody := `{"schema_version":1,"name":"Panel outline","profile_id":"` + profileID + `","profile_revision":1,"rectangle":{"x_mm":10,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`
	w := request(t, s, "POST", "/api/v1/projects", projectBody, "test-key")
	var project server.DesignerProject
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = request(t, s, "POST", "/api/v1/projects/"+project.ID+"/generate", `{"revision":1}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("generate job: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"gcode"`) {
		t.Fatalf("creation response must not echo program bytes: %s", w.Body.String())
	}
	// Program bytes are served only by the single-job endpoint.
	w = request(t, s, "GET", w.Header().Get("Location"), "", "test-key")
	var job struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Gcode      string `json:"gcode"`
		State      string `json:"state"`
		Total      int    `json:"total"`
		Provenance struct {
			GeneratorVersion string                 `json:"generator_version"`
			Project          server.DesignerProject `json:"project"`
			Profile          server.MachineProfile  `json:"profile"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Name != "Panel outline.gcode" || job.Gcode != expectedProgram || job.State != "queued" || job.Total != 11 {
		t.Fatalf("unexpected generated job: %+v", job)
	}
	if job.Provenance.GeneratorVersion != "rectangle-v1" || job.Provenance.Project.ID != project.ID || job.Provenance.Project.Revision != 1 || job.Provenance.Profile.ID != profileID || job.Provenance.Profile.Revision != 1 {
		t.Fatalf("incomplete provenance: %+v", job.Provenance)
	}
	review := request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/review", "", "test-key")
	if review.Code != http.StatusOK || !strings.Contains(review.Body.String(), `"bounds_status":"in_bounds"`) || !strings.Contains(review.Body.String(), `"program":{"segments"`) {
		t.Fatalf("generated program review response is incomplete: %d %s", review.Code, review.Body.String())
	}
	expectedProject := server.DesignerProject{SchemaVersion: 1, ID: project.ID, Revision: 1, Name: "Panel outline", ProfileID: profileID, ProfileRevision: 1,
		UpdatedAt: project.UpdatedAt, UpdatedBy: project.UpdatedBy,
		Rectangle: server.Rectangle{XMM: 10, YMM: 20, WidthMM: 30, HeightMM: 20}, Operation: server.DesignerOperation{Type: "vector-engrave", FeedMMMin: 600, PowerPercent: 25}}
	expectedProfile := server.MachineProfile{SchemaVersion: 1, ID: profileID, Revision: 1, Name: "Declared GRBL", Controller: "grbl-1.1", Units: "mm", Origin: "lower-left", WidthMM: 300, HeightMM: 200, MinS: 0, MaxS: 1000, MinFeed: 1, MaxFeed: 6000, MaxPowerPct: 100, LaserMode: true}
	if !reflect.DeepEqual(job.Provenance.Project, expectedProject) || job.Provenance.Profile != expectedProfile {
		t.Fatalf("provenance did not retain full source/profile snapshots: %+v", job.Provenance)
	}
	if _, err := driver.Validate(job.Gcode); err != nil {
		t.Fatalf("generated job fails controller validation: %v", err)
	}
	if w := request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"missing","confirmed":true,"bounds_override":true}`, "test-key"); w.Code != http.StatusConflict {
		t.Fatalf("generation unexpectedly changed job lifecycle: start returned %d", w.Code)
	}
	w = request(t, s, "PUT", "/api/v1/projects/"+project.ID, `{"schema_version":1,"name":"Changed source","profile_id":"`+profileID+`","profile_revision":1,"rectangle":{"x_mm":11,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25},"revision":1}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("edit source after generation: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, "DELETE", "/api/v1/projects/"+project.ID, `{"revision":2}`, "test-key"); w.Code != http.StatusNoContent {
		t.Fatalf("delete source project: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, "DELETE", "/api/v1/profiles/"+profileID, `{"revision":1}`, "test-key"); w.Code != http.StatusNoContent {
		t.Fatalf("delete source profile: %d %s", w.Code, w.Body.String())
	}
	jobReview := request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/review", "", "test-key")
	if jobReview.Code != http.StatusOK || !strings.Contains(jobReview.Body.String(), `"profile_stale":true`) || !strings.Contains(jobReview.Body.String(), `"bounds_status":"unknown"`) {
		t.Fatalf("generated job review did not report its stale profile: %d %s", jobReview.Code, jobReview.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	var persisted struct {
		Gcode      string          `json:"gcode"`
		Provenance json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &persisted); err != nil || w.Code != http.StatusOK {
		t.Fatalf("read generated job: %d %s %v", w.Code, w.Body.String(), err)
	}
	var finalProv struct {
		Project server.DesignerProject `json:"project"`
		Profile server.MachineProfile  `json:"profile"`
	}
	if err := json.Unmarshal(persisted.Provenance, &finalProv); err != nil {
		t.Fatal(err)
	}
	if persisted.Gcode != expectedProgram || !reflect.DeepEqual(finalProv.Project, expectedProject) || finalProv.Profile != expectedProfile {
		t.Fatalf("source mutation changed immutable generated job: %+v", persisted)
	}
}

func createTestProfile(t *testing.T, h http.Handler) string {
	t.Helper()
	w := request(t, h, "POST", "/api/v1/profiles", profileBody, "test-key")
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create fixture profile: %d %s %v", w.Code, w.Body.String(), err)
	}
	return profile.ID
}

func contains(s, part string) bool { return strings.Contains(s, part) }
