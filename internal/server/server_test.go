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
	if !path.Supported || path.Complete || len(path.Segments) != 1 || path.Segments[0].SourceLine != 4 || !path.Segments[0].LaserOn || path.Segments[0].Rapid || len(path.Warnings) == 0 {
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
	if w.Code != 200 || w.Body.String() != "[]\n" {
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
	if w.Body.String() != "[]\n" {
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
