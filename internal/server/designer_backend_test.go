package server_test

import (
	"encoding/json"
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

func contains(s, part string) bool { return strings.Contains(s, part) }
