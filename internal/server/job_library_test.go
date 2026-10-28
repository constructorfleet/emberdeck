package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

type jobPage struct {
	Jobs   []map[string]any `json:"jobs"`
	Total  int              `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

func listJobs(t *testing.T, h http.Handler, query string) jobPage {
	t.Helper()
	w := request(t, h, "GET", "/api/v1/jobs"+query, "", "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("list %q: %d %s", query, w.Code, w.Body.String())
	}
	var page jobPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatalf("list %q: %v %s", query, err, w.Body.String())
	}
	for _, job := range page.Jobs {
		if _, ok := job["gcode"]; ok {
			t.Fatalf("list %q exposed program bytes: %s", query, w.Body.String())
		}
		if _, ok := job["lease"]; ok {
			t.Fatalf("list %q exposed worker lease: %s", query, w.Body.String())
		}
	}
	return page
}

func uploadJob(t *testing.T, h http.Handler, name, gcode string) string {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"name": name, "gcode": gcode})
	w := request(t, h, "POST", "/api/v1/jobs", string(payload), "test-key")
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), `"gcode"`) {
		t.Fatalf("upload %s must return metadata only: %d %s", name, w.Code, w.Body.String())
	}
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	return job.ID
}

// startOnDriver registers a worker session, dispatches the job, and returns the lease.
func startOnDriver(t *testing.T, h http.Handler, driverID, jobID string) string {
	t.Helper()
	request(t, h, "POST", "/api/v1/drivers/"+driverID+"/poll", `{"session":"`+driverID+`-session"}`, "test-key")
	w := request(t, h, "POST", "/api/v1/jobs/"+jobID+"/start", `{"driver_id":"`+driverID+`","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"lease"`) || strings.Contains(w.Body.String(), `"gcode"`) {
		t.Fatalf("start response exposed lease or program: %s", w.Body.String())
	}
	w = request(t, h, "POST", "/api/v1/drivers/"+driverID+"/poll", `{"session":"`+driverID+`-session"}`, "test-key")
	var assignment struct {
		Command string
		Lease   string
		Job     struct {
			ID    string
			Gcode string
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.Command != "run" || assignment.Job.ID != jobID || assignment.Lease == "" || assignment.Job.Gcode == "" {
		t.Fatalf("worker assignment must carry program and lease: %s", w.Body.String())
	}
	return assignment.Lease
}

func TestJobListIsPaginatedFilterableMetadata(t *testing.T) {
	s := openServer(t)
	ids := []string{}
	for _, name := range []string{"Alpha panel.gcode", "beta.gcode", "ALPHA box.gcode", "gamma.gcode", "delta.gcode"} {
		ids = append(ids, uploadJob(t, s, name, "G21\nG90\nM5"))
		time.Sleep(2 * time.Millisecond)
	}
	startOnDriver(t, s, "bench", ids[1])

	page := listJobs(t, s, "?limit=2")
	if page.Total != 5 || page.Limit != 2 || page.Offset != 0 || len(page.Jobs) != 2 {
		t.Fatalf("first page: %+v", page)
	}
	if page.Jobs[0]["id"] != ids[4] || page.Jobs[1]["id"] != ids[3] {
		t.Fatalf("pages must list newest jobs first: %+v", page.Jobs)
	}
	if page.Jobs[0]["size"] != float64(len("G21\nG90\nM5")) {
		t.Fatalf("metadata must report program size: %+v", page.Jobs[0])
	}
	page = listJobs(t, s, "?limit=2&offset=4")
	if page.Total != 5 || len(page.Jobs) != 1 || page.Jobs[0]["id"] != ids[0] {
		t.Fatalf("last page: %+v", page)
	}
	if page = listJobs(t, s, ""); page.Total != 5 || len(page.Jobs) != 5 || page.Limit != 50 {
		t.Fatalf("default page: %+v", page)
	}
	if page = listJobs(t, s, "?state=running"); page.Total != 1 || page.Jobs[0]["id"] != ids[1] || page.Jobs[0]["driver_id"] != "bench" {
		t.Fatalf("state filter: %+v", page)
	}
	if page = listJobs(t, s, "?state=queued,running"); page.Total != 5 {
		t.Fatalf("multiple state filter: %+v", page)
	}
	if page = listJobs(t, s, "?driver=bench"); page.Total != 1 || page.Jobs[0]["id"] != ids[1] {
		t.Fatalf("driver filter: %+v", page)
	}
	if page = listJobs(t, s, "?name=alpha"); page.Total != 2 || page.Jobs[0]["id"] != ids[2] || page.Jobs[1]["id"] != ids[0] {
		t.Fatalf("case-insensitive name filter: %+v", page)
	}
	if page = listJobs(t, s, "?name=alpha&state=running"); page.Total != 0 || len(page.Jobs) != 0 {
		t.Fatalf("combined filters: %+v", page)
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?limit=x", "?offset=-1", "?state=bogus", "?driver=bad%20id"} {
		if w := request(t, s, "GET", "/api/v1/jobs"+query, "", "test-key"); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
	w := request(t, s, "GET", "/api/v1/jobs/"+ids[1], "", "test-key")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"gcode":"G21\nG90\nM5"`) || strings.Contains(w.Body.String(), `"lease"`) {
		t.Fatalf("job detail must include program but never lease: %s", w.Body.String())
	}
	for _, path := range []string{"/cancel"} {
		w = request(t, s, "POST", "/api/v1/jobs/"+ids[1]+path, `{}`, "test-key")
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"lease"`) {
			t.Fatalf("%s exposed lease: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestProgressOnlyPollsStayInMemoryUntilStateTransition(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id := uploadJob(t, s, "cut.gcode", "G21\nG90\nM5")
	lease := startOnDriver(t, s, "bench", id)

	// Block every durable write of the job library. Progress-only reports must
	// still succeed because they are not written to disk.
	library := filepath.Join(dir, "jobs.json")
	if err := os.Rename(library, library+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(library, 0700); err != nil {
		t.Fatal(err)
	}
	for acked := 1; acked <= 2; acked++ {
		body := fmt.Sprintf(`{"session":"bench-session","job_id":"%s","lease":"%s","state":"running","controller_state":"Run","acked":%d,"total":3}`, id, lease, acked)
		w := request(t, s, "POST", "/api/v1/drivers/bench/poll", body, "test-key")
		if w.Code != http.StatusOK {
			t.Fatalf("progress-only report touched durable storage: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"gcode"`) {
			t.Fatalf("progress acknowledgement resent the program: %s", w.Body.String())
		}
	}
	page := listJobs(t, s, "?state=running")
	if page.Total != 1 || page.Jobs[0]["acked"] != float64(2) || page.Jobs[0]["controller_state"] != "Run" {
		t.Fatalf("in-memory progress not visible: %+v", page)
	}
	completed := fmt.Sprintf(`{"session":"bench-session","job_id":"%s","lease":"%s","state":"completed","acked":3,"total":3}`, id, lease)
	if w := request(t, s, "POST", "/api/v1/drivers/bench/poll", completed, "test-key"); w.Code != http.StatusInternalServerError {
		t.Fatalf("state transition must be durable before acknowledgement: %d %s", w.Code, w.Body.String())
	}
	if page = listJobs(t, s, "?state=running"); page.Total != 1 {
		t.Fatalf("unpersisted transition published: %+v", page)
	}

	if err := os.Remove(library); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(library+".saved", library); err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "POST", "/api/v1/drivers/bench/poll", completed, "test-key"); w.Code != http.StatusOK {
		t.Fatalf("completion: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	if page = listJobs(t, restarted, "?state=completed"); page.Total != 1 || page.Jobs[0]["acked"] != float64(3) {
		t.Fatalf("completed transition not durable: %+v", page)
	}
}

func TestGracefulCloseKeepsLatestInMemoryProgress(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id := uploadJob(t, s, "cut.gcode", "G21\nG90\nM5")
	lease := startOnDriver(t, s, "bench", id)
	body := fmt.Sprintf(`{"session":"bench-session","job_id":"%s","lease":"%s","state":"running","acked":2,"total":3}`, id, lease)
	if w := request(t, s, "POST", "/api/v1/drivers/bench/poll", body, "test-key"); w.Code != http.StatusOK {
		t.Fatalf("progress: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	page := listJobs(t, restarted, "")
	if page.Total != 1 || page.Jobs[0]["state"] != "interrupted" || page.Jobs[0]["acked"] != float64(2) {
		t.Fatalf("graceful shutdown lost last progress: %+v", page)
	}
}

const legacyLibrary = `{
 "0123456789abcdef0123456789abcdef": {"id":"0123456789abcdef0123456789abcdef","name":"old square.gcode","gcode":"G21\nG90\nM5","state":"completed","acked":3,"total":3,"driver_id":"bench","lease":"stale-lease","created":"2026-01-02T03:04:05Z"},
 "fedcba9876543210fedcba9876543210": {"id":"fedcba9876543210fedcba9876543210","name":"old running.gcode","gcode":"G21\nG1 X1 F100\nM5","state":"running","acked":1,"total":3,"driver_id":"bench","lease":"live-lease","created":"2026-01-03T03:04:05Z"}
}`

func TestLegacyJobLibraryMigratesOnStartup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(legacyLibrary), 0600); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		s, err := server.Open(dir, "test-key")
		if err != nil {
			t.Fatalf("open round %d: %v", round, err)
		}
		page := listJobs(t, s, "?name=old")
		if page.Total != 2 || page.Jobs[0]["name"] != "old running.gcode" || page.Jobs[1]["name"] != "old square.gcode" {
			_ = s.Close()
			t.Fatalf("round %d migrated list: %+v", round, page)
		}
		if page.Jobs[0]["state"] != "interrupted" || page.Jobs[1]["state"] != "completed" || page.Jobs[1]["size"] != float64(len("G21\nG90\nM5")) {
			_ = s.Close()
			t.Fatalf("round %d migrated metadata: %+v", round, page)
		}
		w := request(t, s, "GET", "/api/v1/jobs/fedcba9876543210fedcba9876543210", "", "test-key")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"gcode":"G21\nG1 X1 F100\nM5"`) || strings.Contains(w.Body.String(), "lease") {
			_ = s.Close()
			t.Fatalf("round %d migrated detail: %s", round, w.Body.String())
		}
		if round == 0 {
			uploadJob(t, s, "new.gcode", "M5")
		} else if page = listJobs(t, s, "?name=new"); page.Total != 1 {
			_ = s.Close()
			t.Fatalf("post-migration upload lost: %+v", page)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("G21")) {
		t.Fatalf("job metadata still embeds program bytes: %s", data)
	}
}

func TestInvalidLegacyLibraryFailsClosedWithoutRewriting(t *testing.T) {
	cases := map[string]string{
		"malformed":       `{"0123456789abcdef0123456789abcdef":`,
		"mismatched id":   strings.Replace(legacyLibrary, `"id":"0123456789abcdef0123456789abcdef"`, `"id":"11111111111111111111111111111111"`, 1),
		"missing program": strings.Replace(legacyLibrary, `"gcode":"G21\nG90\nM5"`, `"gcode":""`, 1),
		"unknown state":   strings.Replace(legacyLibrary, `"state":"completed"`, `"state":"exploded"`, 1),
		"null":            `null`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "jobs.json")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if s, err := server.Open(dir, "test-key"); err == nil {
				_ = s.Close()
				t.Fatal("invalid legacy library opened")
			}
			if data, _ := os.ReadFile(path); string(data) != content {
				t.Fatalf("failed migration rewrote library: %s", data)
			}
		})
	}
	t.Run("program store unavailable", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "jobs.json")
		if err := os.WriteFile(path, []byte(legacyLibrary), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "programs"), []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		if s, err := server.Open(dir, "test-key"); err == nil {
			_ = s.Close()
			t.Fatal("migration succeeded without a program store")
		}
		if data, _ := os.ReadFile(path); string(data) != legacyLibrary {
			t.Fatalf("failed migration rewrote library: %s", data)
		}
	})
}

func TestMissingProgramFileFailsStartupClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	uploadJob(t, s, "square.gcode", "G21\nG90\nM5")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	programs, err := filepath.Glob(filepath.Join(dir, "programs", "*.gcode"))
	if err != nil || len(programs) != 1 {
		t.Fatalf("expected one stored program file: %v %v", programs, err)
	}
	if err := os.WriteFile(programs[0], []byte("G21\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := server.Open(dir, "test-key"); err == nil {
		_ = s.Close()
		t.Fatal("library with a damaged program opened")
	}
}
