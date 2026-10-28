package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/internal/server"
)

func request(t *testing.T, h http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestUploadRejectsDriverInvalidProgramWithLineNumber(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	longLine := strings.Repeat("G", driver.MaxLineLength+1)
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"too-long.gcode","gcode":"`+longLine+`"}`, "test-key")
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "line 1") {
		t.Fatalf("invalid program upload = %d %s; want 422 with line number", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs", `{"name":"unclosed-comment.gcode","gcode":"G21\nG0 X0 (unterminated"}`, "test-key")
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "line 2") {
		t.Fatalf("unclosed comment upload = %d %s; want 422 with line 2", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs", `{"name":"comments-only.gcode","gcode":"(notes only)"}`, "test-key")
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "line 1") {
		t.Fatalf("comments-only upload = %d %s; want 422 with a line number", w.Code, w.Body.String())
	}
}

func TestUploadValidationDistinguishesNameContentAndBinaryErrors(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	cases := []struct {
		name   string
		body   string
		field  string
		detail string
	}{
		{"blank name", `{"name":"","gcode":"G21\nG90\nM5"}`, "name", "Enter a display name for this program."},
		{"blank program", `{"name":"empty.gcode","gcode":"  \n\t"}`, "gcode", "Enter the program text."},
		{"binary program", `{"name":"image.png","gcode":"G21\n\u0000\u0001M5"}`, "gcode", "Program must be plain text G-code without binary or control bytes."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := request(t, s, "POST", "/api/v1/jobs", tc.body, "test-key")
			var response struct {
				Fields map[string]string `json:"fields"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusBadRequest {
				t.Fatalf("upload validation response: %d %s (%v)", w.Code, w.Body.String(), err)
			}
			if response.Fields[tc.field] != tc.detail {
				t.Fatalf("field %s = %q; want %q (all fields %#v)", tc.field, response.Fields[tc.field], tc.detail, response.Fields)
			}
		})
	}
}

func TestUploadedProgramReviewReportsProfileBoundsAndUnknownMotion(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/profiles", profileBody, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("create profile: %d %s", w.Code, w.Body.String())
	}
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"bounds.gcode","profile_id":"` + profile.ID + `","gcode":"G21 G90\nG0 X0 Y0\nG1 X350 Y100 F300"}`
	w = request(t, s, "POST", "/api/v1/jobs", body, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/review", "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bounds_status":"out_of_bounds"`) || !strings.Contains(w.Body.String(), `"max":{"x":350,"y":100}`) {
		t.Fatalf("out-of-bounds review = %d %s", w.Code, w.Body.String())
	}
	unknownBody := `{"name":"incremental.gcode","profile_id":"` + profile.ID + `","gcode":"G21 G90\nG0 X0 Y0\nG91\nG1 X20 Y20 F300"}`
	w = request(t, s, "POST", "/api/v1/jobs", unknownBody, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload incremental program: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/review", "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bounds_status":"unknown"`) || !strings.Contains(w.Body.String(), "G91") {
		t.Fatalf("incremental motion review = %d %s", w.Code, w.Body.String())
	}
	unsupportedBody := `{"name":"arc.gcode","profile_id":"` + profile.ID + `","gcode":"G21 G90\nG0 X0 Y0\nG2 X350 Y100 I10 J10\n"}`
	w = request(t, s, "POST", "/api/v1/jobs", unsupportedBody, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload unsupported program: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/review", "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bounds_status":"unknown"`) {
		t.Fatalf("unsupported out-of-bounds motion must stay unknown: %d %s", w.Code, w.Body.String())
	}
	updatedProfile := strings.Replace(profileBody, `"name":"Declared GRBL"`, `"revision":1,"name":"Declared GRBL"`, 1)
	updatedProfile = strings.Replace(updatedProfile, `"width_mm":300`, `"width_mm":400`, 1)
	w = request(t, s, "PUT", "/api/v1/profiles/"+profile.ID, updatedProfile, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("update profile: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/review", "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bounds_status":"unknown"`) || !strings.Contains(w.Body.String(), `"profile_stale":true`) || !strings.Contains(w.Body.String(), "changed or was deleted") {
		t.Fatalf("stale profile review = %d %s", w.Code, w.Body.String())
	}
}

func TestUploadedAbsoluteProgramInsideChosenProfileStartsWithoutOverride(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/profiles", profileBody, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("create profile: %d %s", w.Code, w.Body.String())
	}
	var profile struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	upload := func(name, gcode string) string {
		t.Helper()
		w := request(t, s, "POST", "/api/v1/jobs", `{"name":"`+name+`","profile_id":"`+profile.ID+`","gcode":"`+gcode+`"}`, "test-key")
		if w.Code != http.StatusCreated {
			t.Fatalf("upload %s: %d %s", name, w.Code, w.Body.String())
		}
		var job struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	// The first move positions the head with the laser off, so its unknown
	// starting point cannot mark the work area. Every burned point is known.
	inside := upload("inside.gcode", `G21 G90\nG0 X10 Y10\nM4 S100\nG1 X50 Y40 F300\nG1 X10 Y40\nM5\nG0 X0 Y0`)
	w = request(t, s, "GET", "/api/v1/jobs/"+inside+"/review", "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bounds_status":"in_bounds"`) || !strings.Contains(w.Body.String(), `"min":{"x":0,"y":0},"max":{"x":50,"y":40}`) || !strings.Contains(w.Body.String(), `"profile_id":"`+profile.ID+`"`) {
		t.Fatalf("in-bounds review = %d %s", w.Code, w.Body.String())
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
	w = request(t, s, "POST", "/api/v1/jobs/"+inside+"/start", `{"driver_id":"bench","confirmed":true}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("in-bounds start without override = %d %s", w.Code, w.Body.String())
	}

	// A first move that may burn from an unknown position stays unknown.
	burning := upload("burning-start.gcode", `G21 G90\nM4 S100\nG1 X10 Y10 F300\nG1 X50 Y40\nM5`)
	w = request(t, s, "GET", "/api/v1/jobs/"+burning+"/review", "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bounds_status":"unknown"`) || !strings.Contains(w.Body.String(), "unknown position") {
		t.Fatalf("laser-on initial move review = %d %s", w.Code, w.Body.String())
	}

	for name, gcode := range map[string]string{
		"home.gcode":    `G21 G90\nG0 X10 Y10\nG28\nG0 X20 Y20`,
		"second.gcode":  `G21 G90\nG0 X10 Y10\nG30\nG0 X20 Y20`,
		"offset.gcode":  `G21 G90\nG10 L20 P1 X0 Y0\nG0 X10 Y10\nG0 X20 Y20`,
		"machine.gcode": `G21 G90\nG0 X10 Y10\nG53 G0 X20 Y20`,
	} {
		id := upload(name, gcode)
		w = request(t, s, "GET", "/api/v1/jobs/"+id+"/review", "", "test-key")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"bounds_status":"unknown"`) {
			t.Fatalf("%s review must stay unknown = %d %s", name, w.Code, w.Body.String())
		}
	}
}

func TestUploadedJobWithUnknownBoundsRequiresExplicitStartOverride(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"unknown.gcode","gcode":"G21 G90\nG0 X10 Y10\nG1 X20 Y20 F300"}`, "test-key")
	var job struct {
		ID string `json:"id"`
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
	startURL := "/api/v1/jobs/" + job.ID + "/start"
	w = request(t, s, "POST", startURL, `{"driver_id":"bench","confirmed":true}`, "test-key")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "bounds_override") {
		t.Fatalf("start without explicit override = %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", startURL, `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("start with explicit override: %d %s", w.Code, w.Body.String())
	}
}

func TestUploadedJobSurvivesServerRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"square.gcode","gcode":"G21\nG90\nM5"}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.State != "queued" {
		t.Fatalf("unexpected job: %+v", created)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	w = request(t, restarted, "GET", "/api/v1/jobs/"+created.ID, "", "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("read: %d %s", w.Code, w.Body.String())
	}
	var job struct {
		Name  string
		Gcode string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Name != "square.gcode" || job.Gcode != "G21\nG90\nM5" {
		t.Fatalf("lost job: %+v", job)
	}
}

func TestJobToolpathEndpointParsesUploadedProgramWithoutChangingIt(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"panel.gcode","gcode":"G21 G90\nG0 X0 Y0\nM4 S500\nG1 X10 Y0 F300\nM5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/toolpath", "", "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("toolpath: %d %s", w.Code, w.Body.String())
	}
	var path struct {
		Supported bool `json:"supported"`
		Complete  bool `json:"complete"`
		Segments  []struct {
			SourceLine int  `json:"source_line"`
			LaserOn    bool `json:"laser_on"`
			Rapid      bool `json:"rapid"`
		} `json:"segments"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &path); err != nil {
		t.Fatal(err)
	}
	if !path.Supported || !path.Complete || len(path.Segments) != 1 || path.Segments[0].SourceLine != 4 || !path.Segments[0].LaserOn || path.Segments[0].Rapid || len(path.Warnings) != 1 || !strings.Contains(path.Warnings[0], "laser off") {
		t.Fatalf("toolpath does not describe supported geometry and its unknown start honestly: %s", w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/jobs/not-found/toolpath", "", "test-key")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown job toolpath: %d", w.Code)
	}
}

func TestInvalidUploadsDoNotEnterLibrary(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, body := range []string{
		`{}`, `{"name":"x","gcode":" "}`, `{"name":"x","gcode":"G0","extra":true}`,
		`{"name":"x","gcode":"G0"} {}`, `{"name":"x","gcode":"G0\u0000"}`,
		`{"name":"x","gcode":"G0\u0018"}`,
	} {
		w := request(t, s, "POST", "/api/v1/jobs", body, "test-key")
		if w.Code != 400 {
			t.Errorf("accepted invalid upload %s: %d", body, w.Code)
		}
	}
	w := request(t, s, "GET", "/api/v1/jobs", "", "test-key")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"jobs":[]`) || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("invalid jobs persisted: %s", w.Body.String())
	}
}

func TestJobLibraryRequiresAuthentication(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, key := range []string{"", "wrong-key"} {
		w := request(t, s, "GET", "/api/v1/jobs", "", key)
		if w.Code != 401 {
			t.Fatalf("unauthenticated request returned %d", w.Code)
		}
	}
}

func TestFailedPersistenceDoesNotPublishJob(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// A directory at the destination prevents atomic replacement on every OS.
	if err := os.Mkdir(filepath.Join(dir, "jobs.json"), 0700); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"square","gcode":"M5"}`, "test-key")
	if w.Code != 500 {
		t.Fatalf("persistence failure returned %d", w.Code)
	}
	w = request(t, s, "GET", "/api/v1/jobs", "", "test-key")
	if !strings.Contains(w.Body.String(), `"jobs":[]`) || !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("published unpersisted job: %s", w.Body.String())
	}
}

func TestOperatorDeletesQueuedJob(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"scrap","gcode":"M5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	w = request(t, s, "DELETE", "/api/v1/jobs/"+job.ID, "", "test-key")
	if w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	if w.Code != 404 {
		t.Fatalf("deleted job remains visible: %d", w.Code)
	}
}

func TestConsoleLoadsWithoutExposingJobs(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "GET", "/", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "EmberDeck") {
		t.Fatalf("console: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatal("console is not HTML")
	}
	w = request(t, s, "GET", "/api/v1/jobs", "", "")
	if w.Code != 401 {
		t.Fatal("console bypassed authentication")
	}
}

func TestPublicAssetsAndUnknownPaths(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	public := []struct{ path, contentType string }{
		{"/", "text/html"},
		{"/app.js", "text/javascript"},
		{"/style.css", "text/css"},
		{"/job-history.js", "text/javascript"},
		{"/job-history.css", "text/css"},
		{"/favicon.ico", "image/"},
	}
	for _, want := range public {
		for _, method := range []string{"GET", "HEAD"} {
			w := request(t, s, method, want.path, "", "")
			if w.Code != http.StatusOK {
				t.Errorf("%s %s without a key: %d", method, want.path, w.Code)
			}
			if got := w.Header().Get("Content-Type"); !strings.Contains(got, want.contentType) {
				t.Errorf("%s %s content type = %q, want %q", method, want.path, got, want.contentType)
			}
		}
	}

	// Unknown non-API paths are ordinary misses, never authentication failures.
	for _, method := range []string{"GET", "POST", "DELETE"} {
		for _, key := range []string{"", "test-key"} {
			w := request(t, s, method, "/missing-page", "", key)
			if w.Code != http.StatusNotFound {
				t.Errorf("%s /missing-page with key %q returned %d, want 404", method, key, w.Code)
			}
			if strings.Contains(w.Body.String(), "authentication") {
				t.Errorf("%s /missing-page with key %q reported an authentication error: %s", method, key, w.Body.String())
			}
		}
	}

	// The API namespace still authenticates before routing, including unknown routes.
	if w := request(t, s, "GET", "/api/v1/unknown", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated unknown API route returned %d, want 401", w.Code)
	}
	if w := request(t, s, "GET", "/api/v1/unknown", "", "test-key"); w.Code != http.StatusNotFound {
		t.Errorf("authenticated unknown API route returned %d, want 404", w.Code)
	}
}

func TestDataDirectoryHasOneServerOwner(t *testing.T) {
	dir := t.TempDir()
	first, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := server.Open(dir, "test-key")
	if err == nil {
		second.Close()
		t.Fatal("two servers can mutate the same library")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	defer reopened.Close()
}
