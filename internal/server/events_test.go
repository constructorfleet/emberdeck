package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

type jobEvent struct {
	Seq       int64     `json:"seq"`
	Time      time.Time `json:"time"`
	JobID     string    `json:"job_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Source    string    `json:"source"`
	Action    string    `json:"action"`
	DriverID  string    `json:"driver_id"`
	SessionID string    `json:"session_id"`
	LeaseID   string    `json:"lease_id"`
	Detail    string    `json:"detail"`
}

// jobEvents fetches a job's history and checks that no worker credential leaks.
func jobEvents(t *testing.T, h http.Handler, id string, secrets ...string) []jobEvent {
	t.Helper()
	w := request(t, h, "GET", "/api/v1/jobs/"+id+"/events", "", "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("events %s: %d %s", id, w.Code, w.Body.String())
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(w.Body.String(), secret) {
			t.Fatalf("event history exposed worker credential %q: %s", secret, w.Body.String())
		}
	}
	var body struct {
		Events []jobEvent `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("events %s: %v %s", id, err, w.Body.String())
	}
	if body.Events == nil {
		t.Fatalf("events must be a JSON array: %s", w.Body.String())
	}
	return body.Events
}

type step struct{ from, to, source, action string }

func requireHistory(t *testing.T, events []jobEvent, want []step) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("history has %d events, want %d: %+v", len(events), len(want), events)
	}
	for i, event := range events {
		w := want[i]
		if event.From != w.from || event.To != w.to || event.Source != w.source || event.Action != w.action {
			t.Fatalf("event %d = %s→%s by %s/%s, want %s→%s by %s/%s", i, event.From, event.To, event.Source, event.Action, w.from, w.to, w.source, w.action)
		}
		if event.Time.IsZero() || event.Seq <= 0 || (i > 0 && (event.Seq <= events[i-1].Seq || event.Time.Before(events[i-1].Time))) {
			t.Fatalf("event %d is not ordered and timestamped: %+v", i, events)
		}
	}
}

func TestEveryOperatorAndWorkerTransitionIsRecorded(t *testing.T) {
	s := openServer(t)
	id, lease := assignControlJob(t, s, true)
	request(t, s, "POST", "/api/v1/jobs/"+id+"/pause", `{}`, "test-key")
	if w := reportControl(t, s, id, lease, "paused", "Hold:0"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	// A repeated report and a progress-only report are not transitions.
	reportControl(t, s, id, lease, "paused", "Hold:0")
	request(t, s, "POST", "/api/v1/jobs/"+id+"/resume", `{"confirmed":true}`, "test-key")
	if w := reportControl(t, s, id, lease, "running", "Run"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	reportControl(t, s, id, lease, "running", "Run")
	request(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", `{}`, "test-key")
	if w := reportControl(t, s, id, lease, "canceled", "Idle"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := request(t, s, "DELETE", "/api/v1/jobs/"+id, "", "test-key"); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	// History outlives deletion so an incident can still be reconstructed.
	events := jobEvents(t, s, id, lease, "owner")
	requireHistory(t, events, []step{
		{"", "queued", "operator", "upload"},
		{"queued", "running", "operator", "start"},
		{"running", "pausing", "operator", "pause"},
		{"pausing", "paused", "worker", "report"},
		{"paused", "resuming", "operator", "resume"},
		{"resuming", "running", "worker", "report"},
		{"running", "canceling", "operator", "cancel"},
		{"canceling", "canceled", "worker", "report"},
		{"canceled", "deleted", "operator", "delete"},
	})
	if events[0].DriverID != "" || events[0].LeaseID != "" {
		t.Fatalf("upload event names a driver or lease: %+v", events[0])
	}
	leaseID := events[1].LeaseID
	for _, event := range events[1:] {
		if event.JobID != id || event.DriverID != "bench" || event.LeaseID == "" || event.LeaseID != leaseID {
			t.Fatalf("dispatched-job event lacks its driver or stable lease reference: %+v", event)
		}
	}
	sessionID := events[3].SessionID
	if sessionID == "" || events[1].SessionID != sessionID || events[5].SessionID != sessionID {
		t.Fatalf("worker session reference missing or unstable: %+v", events)
	}
}

func TestWorkerOutcomesAndQueuedCancellationAreRecordedWithTimes(t *testing.T) {
	s := openServer(t)
	queued := uploadJob(t, s, "never-run.gcode", "M5")
	if w := request(t, s, "POST", "/api/v1/jobs/"+queued+"/cancel", `{}`, "test-key"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	requireHistory(t, jobEvents(t, s, queued), []step{{"", "queued", "operator", "upload"}, {"queued", "canceled", "operator", "cancel"}})

	completed := uploadJob(t, s, "done.gcode", "M5")
	lease := startOnDriver(t, s, "bench", completed)
	report := `{"session":"bench-session","job_id":"` + completed + `","lease":"` + lease + `","state":"completed","acked":1,"total":1}`
	if w := request(t, s, "POST", "/api/v1/drivers/bench/poll", report, "test-key"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	requireHistory(t, jobEvents(t, s, completed, lease), []step{{"", "queued", "operator", "upload"}, {"queued", "running", "operator", "start"}, {"running", "completed", "worker", "report"}})

	failed := uploadJob(t, s, "broken.gcode", "M5")
	lease = startOnDriver(t, s, "bench", failed)
	report = `{"session":"bench-session","job_id":"` + failed + `","lease":"` + lease + `","state":"failed","acked":0,"total":1,"error":"controller alarm 2"}`
	if w := request(t, s, "POST", "/api/v1/drivers/bench/poll", report, "test-key"); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	events := jobEvents(t, s, failed, lease)
	requireHistory(t, events, []step{{"", "queued", "operator", "upload"}, {"queued", "running", "operator", "start"}, {"running", "failed", "worker", "report"}})
	if events[2].Detail != "controller alarm 2" {
		t.Fatalf("failure event lacks worker error: %+v", events[2])
	}

	// Lists show start, end, and duration so an operator can compare runs.
	for _, job := range listJobs(t, s, "").Jobs {
		started, hasStart := job["started"].(string)
		ended, hasEnd := job["ended"].(string)
		switch job["id"] {
		case queued:
			if hasStart || !hasEnd {
				t.Fatalf("canceled queued job times: %+v", job)
			}
		case completed, failed:
			start, err1 := time.Parse(time.RFC3339Nano, started)
			end, err2 := time.Parse(time.RFC3339Nano, ended)
			duration, hasDuration := job["duration_ms"].(float64)
			if !hasStart || !hasEnd || err1 != nil || err2 != nil || end.Before(start) || !hasDuration || duration != float64(end.Sub(start).Milliseconds()) {
				t.Fatalf("finished job lacks consistent start/end/duration: %+v", job)
			}
		}
	}
}

func TestDesignerGeneratedJobRecordsGenerateEvent(t *testing.T) {
	s := openServer(t)
	profile := createTestProfile(t, s)
	w := request(t, s, "POST", "/api/v1/projects", `{"schema_version":1,"name":"plate","profile_id":"`+profile+`","profile_revision":1,"rectangle":{"x_mm":10,"y_mm":20,"width_mm":30,"height_mm":20},"operation":{"type":"vector-engrave","feed_mm_min":600,"power_percent":25}}`, "test-key")
	var project struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil || project.ID == "" {
		t.Fatalf("create project: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/projects/"+project.ID+"/generate", `{"revision":1}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("generate: %d %s", w.Code, w.Body.String())
	}
	requireHistory(t, jobEvents(t, s, job.ID), []step{{"", "queued", "operator", "generate"}})
}

func TestServerInterruptionsAreRecordedAndHistorySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	id := uploadJob(t, s, "restart.gcode", "M5")
	lease := startOnDriver(t, s, "bench", id)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	events := jobEvents(t, restarted, id, lease)
	requireHistory(t, events, []step{{"", "queued", "operator", "upload"}, {"queued", "running", "operator", "start"}, {"running", "interrupted", "server", "restart"}})
	if events[2].DriverID != "bench" || events[2].LeaseID != events[1].LeaseID || events[2].Detail == "" {
		t.Fatalf("restart interruption lacks driver, revoked lease, or reason: %+v", events[2])
	}
	if w := request(t, restarted, "GET", "/api/v1/jobs/0123456789abcdef0123456789abcdef/events", "", "test-key"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown job history: %d %s", w.Code, w.Body.String())
	}
}

func TestHeartbeatExpiryIsRecordedAsServerTransition(t *testing.T) {
	t.Parallel()
	s := openServer(t)
	id, lease := assignControlJob(t, s, false)
	deadline := time.Now().Add(15 * time.Second)
	for {
		events := jobEvents(t, s, id, lease, "owner")
		if len(events) == 3 {
			requireHistory(t, events, []step{{"", "queued", "operator", "upload"}, {"queued", "running", "operator", "start"}, {"running", "interrupted", "server", "heartbeat_expired"}})
			if events[2].DriverID != "bench" || events[2].SessionID != events[1].SessionID || events[2].LeaseID != events[1].LeaseID {
				t.Fatalf("expiry event lacks driver session or lease reference: %+v", events)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("heartbeat expiry was not recorded: %+v", events)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestTransitionsWriteStructuredLogs(t *testing.T) {
	var logs bytes.Buffer
	s, err := server.OpenWithOptions(t.TempDir(), "test-key", server.Options{Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	id := uploadJob(t, s, "logged.gcode", "M5")
	lease := startOnDriver(t, s, "bench", id)
	if strings.Contains(logs.String(), lease) || strings.Contains(logs.String(), "bench-session") {
		t.Fatalf("logs exposed worker credentials: %s", logs.String())
	}
	var transitions []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line is not structured JSON: %q", line)
		}
		if entry["msg"] == "job state changed" {
			transitions = append(transitions, entry)
		}
	}
	if len(transitions) != 2 {
		t.Fatalf("want 2 transition log entries: %s", logs.String())
	}
	start := transitions[1]
	if start["job_id"] != id || start["from"] != "queued" || start["name"] != "logged.gcode" || start["to"] != "running" || start["source"] != "operator" || start["action"] != "start" || start["driver_id"] != "bench" || start["lease_id"] == nil || start["seq"] == nil {
		t.Fatalf("start log entry lacks transition fields: %+v", start)
	}
}

func TestEventRetentionIsBoundedAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	options := server.Options{EventLimit: 4}
	s, err := server.OpenWithOptions(dir, "test-key", options)
	if err != nil {
		t.Fatal(err)
	}
	first := uploadJob(t, s, "first.gcode", "M5")
	request(t, s, "POST", "/api/v1/jobs/"+first+"/cancel", `{}`, "test-key")
	second := uploadJob(t, s, "second.gcode", "M5")
	request(t, s, "POST", "/api/v1/jobs/"+second+"/cancel", `{}`, "test-key")
	third := uploadJob(t, s, "third.gcode", "M5")
	request(t, s, "POST", "/api/v1/jobs/"+third+"/cancel", `{}`, "test-key")
	check := func(h http.Handler) {
		t.Helper()
		if events := jobEvents(t, h, first); len(events) != 0 {
			t.Fatalf("oldest events exceeded the retention limit: %+v", events)
		}
		requireHistory(t, jobEvents(t, h, second), []step{{"", "queued", "operator", "upload"}, {"queued", "canceled", "operator", "cancel"}})
		requireHistory(t, jobEvents(t, h, third), []step{{"", "queued", "operator", "upload"}, {"queued", "canceled", "operator", "cancel"}})
	}
	check(s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.OpenWithOptions(dir, "test-key", options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	check(restarted)
	lines := countLines(t, filepath.Join(dir, "job-events.jsonl"))
	if lines > 1+4 {
		t.Fatalf("event log holds %d lines after compaction, want header plus at most 4 events", lines)
	}
}

func TestEventAgeRetentionPrunesOldHistory(t *testing.T) {
	s, err := server.OpenWithOptions(t.TempDir(), "test-key", server.Options{EventRetention: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	old := uploadJob(t, s, "old.gcode", "M5")
	time.Sleep(300 * time.Millisecond)
	fresh := uploadJob(t, s, "fresh.gcode", "M5")
	if events := jobEvents(t, s, old); len(events) != 0 {
		t.Fatalf("expired events are still served: %+v", events)
	}
	if events := jobEvents(t, s, fresh); len(events) != 1 {
		t.Fatalf("fresh events were pruned: %+v", events)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Count(data, []byte("\n"))
}

// historyLibrary returns a closed data directory with one dispatched job.
func historyLibrary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	id := uploadJob(t, s, "audit.gcode", "M5")
	request(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", `{}`, "test-key")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDamagedEventHistoryFailsStartupClosed(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"missing": func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		},
		"corrupt committed event": func(t *testing.T, path string) {
			data, _ := os.ReadFile(path)
			if err := os.WriteFile(path, bytes.Replace(data, []byte(`"to":"canceled"`), []byte(`"to":"canceled"!`), 1), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"truncated committed event": func(t *testing.T, path string) {
			data, _ := os.ReadFile(path)
			lines := bytes.SplitAfter(data, []byte("\n"))
			if err := os.WriteFile(path, bytes.Join(lines[:len(lines)-2], nil), 0600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			dir := historyLibrary(t)
			path := filepath.Join(dir, "job-events.jsonl")
			damage(t, path)
			before, _ := os.ReadFile(path)
			index, _ := os.ReadFile(filepath.Join(dir, "jobs.json"))
			if s, err := server.Open(dir, "test-key"); err == nil {
				_ = s.Close()
				t.Fatal("library with damaged event history opened")
			}
			after, _ := os.ReadFile(path)
			afterIndex, _ := os.ReadFile(filepath.Join(dir, "jobs.json"))
			if !bytes.Equal(before, after) || !bytes.Equal(index, afterIndex) {
				t.Fatal("failed startup rewrote the library or its history")
			}
		})
	}
}

func TestUncommittedEventTailIsDiscardedOnStartup(t *testing.T) {
	dir := historyLibrary(t)
	path := filepath.Join(dir, "job-events.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	// A crash after appending events but before committing jobs.json leaves
	// complete and torn lines beyond the committed sequence.
	_, _ = f.WriteString(`{"seq":99,"time":"2026-01-01T00:00:00Z","job_id":"0123456789abcdef0123456789abcdef","to":"queued","source":"operator","action":"upload"}` + "\n" + `{"seq":100,"ti`)
	_ = f.Close()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatalf("uncommitted tail blocked startup: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if w := request(t, s, "GET", "/api/v1/jobs/0123456789abcdef0123456789abcdef/events", "", "test-key"); w.Code != http.StatusNotFound {
		t.Fatalf("uncommitted event was published: %s", w.Body.String())
	}
	id := uploadJob(t, s, "after.gcode", "M5")
	if events := jobEvents(t, s, id); len(events) != 1 || events[0].Seq != 3 {
		t.Fatalf("sequence did not continue from the committed history: %+v", events)
	}
}

func TestLibraryWithoutHistoryUpgradesWithEmptyHistories(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(legacyLibrary), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Existing jobs have no reconstructed history; only the restart recovery
	// of the job that was running is a new, real transition.
	if events := jobEvents(t, s, "0123456789abcdef0123456789abcdef"); len(events) != 0 {
		t.Fatalf("migration invented history: %+v", events)
	}
	requireHistory(t, jobEvents(t, s, "fedcba9876543210fedcba9876543210"), []step{{"running", "interrupted", "server", "restart"}})
}

func TestFailedIndexCommitDoesNotPublishEvents(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := os.Mkdir(filepath.Join(dir, "jobs.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "POST", "/api/v1/jobs", `{"name":"square","gcode":"M5"}`, "test-key"); w.Code != 500 {
		t.Fatalf("persistence failure returned %d", w.Code)
	}
	if err := os.Remove(filepath.Join(dir, "jobs.json")); err != nil {
		t.Fatal(err)
	}
	id := uploadJob(t, s, "after.gcode", "M5")
	if events := jobEvents(t, s, id); len(events) != 1 || events[0].Seq != 1 {
		t.Fatalf("rolled-back event left a sequence gap or was published: %+v", events)
	}
	if lines := countLines(t, filepath.Join(dir, "job-events.jsonl")); lines != 2 {
		t.Fatalf("event log has %d lines, want header plus one committed event", lines)
	}
}

func TestEventsNameTheActingCredential(t *testing.T) {
	s, err := server.Open(t.TempDir(), bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	operator := createCredential(t, s, "night shift", "operator", nil)
	worker := createCredential(t, s, "bench worker", "worker", []string{"bench"})
	viewer := createCredential(t, s, "auditor", "viewer", nil)
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"audit.gcode","gcode":"M5"}`, operator.Key)
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"bench-session"}`, worker.Key)
	if w := request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, operator.Key); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"bench-session"}`, worker.Key)
	var assignment struct{ Lease string }
	_ = json.Unmarshal(w.Body.Bytes(), &assignment)
	report := `{"session":"bench-session","job_id":"` + job.ID + `","lease":"` + assignment.Lease + `","state":"completed","acked":1,"total":1}`
	if w := request(t, s, "POST", "/api/v1/drivers/bench/poll", report, worker.Key); w.Code != 200 {
		t.Fatalf("complete: %d %s", w.Code, w.Body.String())
	}
	// Viewers may read the audit trail; it never contains credential secrets.
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID+"/events", "", viewer.Key)
	if w.Code != http.StatusOK {
		t.Fatalf("viewer events: %d %s", w.Code, w.Body.String())
	}
	for _, secret := range []string{operator.Key, worker.Key, assignment.Lease, "bench-session"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("events exposed a secret: %s", w.Body.String())
		}
	}
	var body struct {
		Events []struct {
			Action         string `json:"action"`
			CredentialID   string `json:"credential_id"`
			CredentialName string `json:"credential_name"`
		} `json:"events"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Events) != 3 {
		t.Fatalf("events: %v %s", err, w.Body.String())
	}
	want := []issuedCredential{operator, operator, worker}
	for i, event := range body.Events {
		if event.CredentialID != want[i].ID || event.CredentialName != want[i].Name {
			t.Fatalf("%s event credential = %q/%q, want %q/%q", event.Action, event.CredentialID, event.CredentialName, want[i].ID, want[i].Name)
		}
	}
}
