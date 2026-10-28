package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/internal/server"
	"github.com/ConstructorFleet/emberdeck/worker"
)

// logBuffer collects JSON log records written concurrently by HTTP handlers.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		records = append(records, record)
	}
	return records
}

func (b *logBuffer) raw() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func find(records []map[string]any, match func(map[string]any) bool) map[string]any {
	for _, record := range records {
		if match(record) {
			return record
		}
	}
	return nil
}

func loggedServer(t *testing.T) (*server.Server, *logBuffer) {
	t.Helper()
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	logs := &logBuffer{}
	s.SetLogger(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return s, logs
}

func TestRequestsAreLoggedWithLevelByOutcome(t *testing.T) {
	s, logs := loggedServer(t)
	if w := request(t, s, "POST", "/api/v1/jobs", `{"name":"a.gcode","gcode":"G21\nM5"}`, "test-key"); w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, "GET", "/api/v1/jobs?limit=5&secret=hidden", "", "test-key"); w.Code != 200 {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, "GET", "/api/v1/jobs", "", "wrong-key"); w.Code != 401 {
		t.Fatalf("bad key: %d", w.Code)
	}
	records := logs.records(t)
	request := func(method, level string, status float64) map[string]any {
		return find(records, func(r map[string]any) bool {
			return r["msg"] == "http request" && r["method"] == method && r["level"] == level && r["status"] == status
		})
	}
	if r := request("POST", "INFO", 201); r == nil || r["path"] != "/api/v1/jobs" || r["duration"] == nil || r["remote"] == nil {
		t.Errorf("successful mutation not logged at info with path, duration, and remote: %v", records)
	}
	if r := request("GET", "DEBUG", 200); r == nil || r["path"] != "/api/v1/jobs" {
		t.Errorf("successful read not logged at debug: %v", records)
	}
	if request("GET", "WARN", 401) == nil {
		t.Errorf("rejected credential not logged at warn: %v", records)
	}
	if raw := logs.raw(); strings.Contains(raw, "hidden") || strings.Contains(raw, "test-key") || strings.Contains(raw, "wrong-key") {
		t.Errorf("logs leaked a query string or credential: %s", raw)
	}
}

func TestServerWithoutLoggerStaysSilent(t *testing.T) {
	s, err := server.Open(t.TempDir(), "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if w := request(t, s, "GET", "/api/v1/jobs", "", "test-key"); w.Code != 200 {
		t.Fatalf("list without logger: %d", w.Code)
	}
}

func TestJobStateChangesAreLogged(t *testing.T) {
	s, logs := loggedServer(t)
	remote := httptest.NewServer(s)
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- worker.Run(ctx, remote.URL, "test-key", "simulator", func(context.Context) (io.ReadWriteCloser, error) { return driver.NewSimulator(), nil })
	}()
	defer func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}()
	await(t, func() bool {
		return strings.Contains(request(t, s, "GET", "/api/v1/drivers", "", "test-key").Body.String(), `"id":"simulator"`)
	})
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"logged.gcode","gcode":"G21\nG90\nG1 X10 F100\nM5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"simulator","confirmed":true,"bounds_override":true}`, "test-key"); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	transition := func(from, to string) map[string]any {
		return find(logs.records(t), func(r map[string]any) bool {
			return r["msg"] == "job state changed" && r["job_id"] == job.ID && r["from"] == from && r["to"] == to
		})
	}
	await(t, func() bool { return transition("running", "completed") != nil })
	if r := transition("new", "queued"); r == nil || r["name"] != "logged.gcode" || r["level"] != "INFO" {
		t.Errorf("upload transition missing job name: %v", r)
	}
	if r := transition("queued", "running"); r == nil || r["driver_id"] != "simulator" {
		t.Errorf("dispatch transition missing driver: %v", r)
	}
}
