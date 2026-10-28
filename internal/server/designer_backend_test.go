package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

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
		`{"id":"spoofed","schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`,
		`{"schema_version":1,"name":"x","controller":"ruida","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`,
		`{"schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300.0001,"height_mm":200,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true}`,
		`{"schema_version":1,"name":"x","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":false}`,
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
	profileUpdate := `{"schema_version":1,"name":"Declared GRBL v2","controller":"grbl-1.1","units":"mm","origin":"lower-left","width_mm":300,"height_mm":200,"min_s":0,"max_s":1000,"min_feed_mm_min":1,"max_feed_mm_min":6000,"max_power_percent":100,"laser_mode_enabled":true,"revision":1}`
	w = request(t, s, "PUT", "/api/v1/profiles/"+profileID, profileUpdate, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("update referenced profile: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "DELETE", "/api/v1/profiles/"+profileID, `{"revision":2}`, "test-key")
	if w.Code != http.StatusConflict {
		t.Fatalf("deleted referenced profile: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/projects/"+project.ID, "", "test-key")
	if w.Code != http.StatusOK || !contains(w.Body.String(), `"profile_stale":true`) {
		t.Fatalf("project did not report stale profile: %d %s", w.Code, w.Body.String())
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
