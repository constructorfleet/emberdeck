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
	if w.Code != http.StatusCreated || !strings.Contains(w.Body.String(), `F600 S5`) {
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
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s %v", w.Code, w.Body.String(), err)
	}
	w = request(t, s, "POST", "/api/v1/projects/"+project.ID+"/generate", `{"revision":1}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("generate job: %d %s", w.Code, w.Body.String())
	}
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
	if review.Code != http.StatusOK || !strings.Contains(review.Body.String(), `"bounds_status":"unknown"`) || !strings.Contains(review.Body.String(), `"program":{"segments"`) {
		t.Fatalf("generated program review response is incomplete: %d %s", review.Code, review.Body.String())
	}
	expectedProject := server.DesignerProject{SchemaVersion: 1, ID: project.ID, Revision: 1, Name: "Panel outline", ProfileID: profileID, ProfileRevision: 1,
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
