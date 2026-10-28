package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

const (
	matchingSettings   = `{"$20":0,"$30":1000,"$31":0,"$32":1,"$130":300,"$131":200}`
	mismatchedSettings = `{"$20":1,"$30":255,"$31":0,"$32":0,"$130":400,"$131":300}`
)

func generateTestJob(t *testing.T, h http.Handler) string {
	t.Helper()
	profileID := createTestProfile(t, h)
	w := request(t, h, "POST", "/api/v1/projects", `{"schema_version":1,"name":"Settings check","profile_id":"`+profileID+`","profile_revision":1,"rectangle":{"x_mm":10,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`, "test-key")
	var project struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	w = request(t, h, "POST", "/api/v1/projects/"+project.ID+"/generate", `{"revision":1}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != http.StatusCreated {
		t.Fatalf("generate: %d %s", w.Code, w.Body.String())
	}
	return job.ID
}

func pollWith(t *testing.T, h http.Handler, fields string) map[string]any {
	t.Helper()
	body := `{"session":"laser-session"` + fields + `}`
	w := request(t, h, "POST", "/api/v1/drivers/laser/poll", body, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("poll %s: %d %s", body, w.Code, w.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func startOnLaser(t *testing.T, h http.Handler, jobID string) *httpResult {
	t.Helper()
	w := request(t, h, "POST", "/api/v1/jobs/"+jobID+"/start", `{"driver_id":"laser","confirmed":true,"bounds_override":true}`, "test-key")
	return &httpResult{code: w.Code, body: w.Body.String()}
}

type httpResult struct {
	code int
	body string
}

func TestGeneratedJobStartComparesControllerSettingsWithProfileSnapshot(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	jobID := generateTestJob(t, s)

	// A new worker session is asked to read settings before anything is known.
	if idle := pollWith(t, s, ""); idle["command"] != "idle" || idle["read_settings"] != true {
		t.Fatalf("idle poll without settings = %v; want a read_settings request", idle)
	}
	if again := pollWith(t, s, ""); again["read_settings"] == true {
		t.Fatalf("settings read re-requested on the next poll: %v", again)
	}
	if r := startOnLaser(t, s, jobID); r.code != http.StatusConflict || !strings.Contains(r.body, "has not reported controller settings") {
		t.Fatalf("start without reported settings = %d %s; want 409", r.code, r.body)
	}

	pollWith(t, s, `,"controller_settings":`+mismatchedSettings+`,"settings_read_at":"2026-01-01T00:00:00Z"`)
	r := startOnLaser(t, s, jobID)
	if r.code != http.StatusConflict {
		t.Fatalf("start with mismatched settings = %d %s; want 409", r.code, r.body)
	}
	var failure struct{ Error string }
	json.Unmarshal([]byte(r.body), &failure)
	for _, field := range []string{"$30 (maximum spindle): controller 255, profile 1000", "$32 (laser mode): controller 0, profile 1"} {
		if !strings.Contains(failure.Error, field) {
			t.Fatalf("mismatch message %q lacks %q", failure.Error, field)
		}
	}
	if strings.Contains(failure.Error, "$31") {
		t.Fatalf("matching $31 reported as a mismatch: %s", failure.Error)
	}
	if job := request(t, s, "GET", "/api/v1/jobs/"+jobID, "", "test-key"); !strings.Contains(job.Body.String(), `"state":"queued"`) {
		t.Fatalf("refused job left queued state: %s", job.Body.String())
	}

	drivers := request(t, s, "GET", "/api/v1/drivers", "", "test-key")
	var listed []struct {
		ID                 string             `json:"id"`
		ControllerSettings map[string]float64 `json:"controller_settings"`
		SettingsReadAt     string             `json:"settings_read_at"`
	}
	if err := json.Unmarshal(drivers.Body.Bytes(), &listed); err != nil || len(listed) != 1 {
		t.Fatalf("drivers = %s", drivers.Body.String())
	}
	if got := listed[0].ControllerSettings; got["$20"] != 1 || got["$30"] != 255 || got["$32"] != 0 || got["$130"] != 400 || got["$131"] != 300 || listed[0].SettingsReadAt == "" {
		t.Fatalf("driver settings = %+v", listed[0])
	}

	pollWith(t, s, `,"controller_settings":`+matchingSettings)
	if r := startOnLaser(t, s, jobID); r.code != http.StatusOK {
		t.Fatalf("start with matching settings = %d %s", r.code, r.body)
	}
	assignment := pollWith(t, s, `,"controller_settings":`+matchingSettings)
	job, _ := assignment["job"].(map[string]any)
	expected, _ := job["expected_settings"].(map[string]any)
	if assignment["command"] != "run" || expected["$30"] != 1000.0 || expected["$31"] != 0.0 || expected["$32"] != 1.0 || len(expected) != 3 {
		t.Fatalf("assignment expected_settings = %v", job["expected_settings"])
	}
}

func TestSettingsErrorBlocksGeneratedJobAndIsVisible(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	jobID := generateTestJob(t, s)
	pollWith(t, s, `,"settings_error":"driver: controller alarm during settings read: ALARM:1"`)
	if r := startOnLaser(t, s, jobID); r.code != http.StatusConflict || !strings.Contains(r.body, "ALARM:1") {
		t.Fatalf("start after settings error = %d %s", r.code, r.body)
	}
	if drivers := request(t, s, "GET", "/api/v1/drivers", "", "test-key"); !strings.Contains(drivers.Body.String(), `"settings_error":"driver: controller alarm during settings read: ALARM:1"`) {
		t.Fatalf("drivers = %s", drivers.Body.String())
	}
}

func TestUploadedJobStartsWithoutSettingsComparisonAndReviewFlagsM3(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	constant := uploadJob(t, s, "constant.gcode", "G21 G90\nG0 X0 Y0\nM03 S100 (constant)\nG1 X1 F100\nM5")
	dynamic := uploadJob(t, s, "dynamic.gcode", "G21 G90\nG0 X0 Y0\nM4 S100 ; not M3\nG1 X1 F100\nM5")
	for id, want := range map[string]bool{constant: true, dynamic: false} {
		w := request(t, s, "GET", "/api/v1/jobs/"+id, "", "test-key")
		var job struct {
			Review struct {
				ConstantPower *bool `json:"constant_power_m3"`
			} `json:"program_review"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.Review.ConstantPower == nil || *job.Review.ConstantPower != want {
			t.Fatalf("job %s constant_power_m3 = %v, want %v: %s", id, job.Review.ConstantPower, want, w.Body.String())
		}
	}
	pollWith(t, s, `,"controller_settings":`+mismatchedSettings)
	if r := startOnLaser(t, s, constant); r.code != http.StatusOK {
		t.Fatalf("uploaded job start = %d %s; settings are a warning, not a block", r.code, r.body)
	}
	assignment := pollWith(t, s, `,"controller_settings":`+mismatchedSettings)
	job, _ := assignment["job"].(map[string]any)
	if _, ok := job["expected_settings"]; ok || assignment["command"] != "run" {
		t.Fatalf("uploaded assignment = %v; want no profile expectation", assignment)
	}
}

func TestInvalidControllerSettingsReportIsRejected(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, fields := range []string{
		`,"controller_settings":{"$32":2}`,
		`,"controller_settings":{"$20":-1}`,
		`,"controller_settings":{"$99":1}`,
		`,"settings_error":"` + strings.Repeat("x", 1001) + `"`,
	} {
		w := request(t, s, "POST", "/api/v1/drivers/laser/poll", `{"session":"laser-session"`+fields+`}`, "test-key")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("poll %s = %d %s; want 400", fields, w.Code, w.Body.String())
		}
	}
}

func TestConsoleServesControllerSettingsScript(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "GET", "/controller-settings.js", "", "")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(w.Body.String(), "EmberDeckControllerSettings") {
		t.Fatalf("controller settings script = %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	page := request(t, s, "GET", "/", "", "")
	if !strings.Contains(page.Body.String(), `<script src="/controller-settings.js" defer></script>`) || !strings.Contains(page.Body.String(), `id="controller-settings"`) {
		t.Fatal("console does not load the controller settings script and status")
	}
}
