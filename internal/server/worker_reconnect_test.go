package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/internal/server"
	"github.com/ConstructorFleet/emberdeck/worker"
)

// liveServer serves one job library on a fixed loopback address so it can be
// stopped and started again at the same URL, like a service restart.
type liveServer struct {
	t       *testing.T
	dir     string
	address string
	handler *server.Server
	http    *http.Server
}

func startLiveServer(t *testing.T, dir, address string) *liveServer {
	t.Helper()
	handler, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	var listener net.Listener
	for deadline := time.Now().Add(5 * time.Second); ; {
		listener, err = net.Listen("tcp", address)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	s := &liveServer{t: t, dir: dir, address: listener.Addr().String(), handler: handler, http: &http.Server{Handler: handler}}
	go func() { _ = s.http.Serve(listener) }()
	t.Cleanup(s.stop)
	return s
}

func (s *liveServer) url() string { return "http://" + s.address }

func (s *liveServer) stop() {
	if s.http == nil {
		return
	}
	_ = s.http.Close()
	_ = s.handler.Close()
	s.http = nil
}

func (s *liveServer) drivers() string {
	return request(s.t, s.handler, "GET", "/api/v1/drivers", "", "test-key").Body.String()
}

type workerLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *workerLog) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *workerLog) contains(fragment string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, fragment) {
			return true
		}
	}
	return false
}

func startWorker(t *testing.T, url, id string, logs *workerLog) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.RunWithOptions(ctx, url, "test-key", id, func(context.Context) (io.ReadWriteCloser, error) {
			return driver.NewSimulator(), nil
		}, worker.Options{RetryMin: 10 * time.Millisecond, RetryMax: 100 * time.Millisecond, Logf: logs.Logf})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Errorf("worker exited with %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	})
}

func eventually(t *testing.T, timeout time.Duration, what string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestIdleWorkerSurvivesServerRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := startLiveServer(t, dir, "127.0.0.1:0")
	logs := &workerLog{}
	startWorker(t, first.url(), "bench", logs)
	eventually(t, 5*time.Second, "worker registration", func() bool { return strings.Contains(first.drivers(), `"id":"bench"`) })

	first.stop()
	eventually(t, 5*time.Second, "a logged retry while the server is down", func() bool { return logs.contains("idle poll failed") })
	second := startLiveServer(t, dir, first.address)
	eventually(t, 5*time.Second, "worker re-registration after restart", func() bool { return strings.Contains(second.drivers(), `"id":"bench"`) })

	// The reconnected worker still executes new work.
	data, _ := json.Marshal(map[string]string{"name": "after-restart.gcode", "gcode": "G21\nG1 X1 F100"})
	w := request(t, second.handler, "POST", "/api/v1/jobs", string(data), "test-key")
	var job struct{ ID, State string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, second.handler, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true}`, "test-key"); w.Code != 200 {
		t.Fatalf("dispatch: %d %s", w.Code, w.Body.String())
	}
	eventually(t, 10*time.Second, "job completion after restart", func() bool {
		w := request(t, second.handler, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
		_ = json.Unmarshal(w.Body.Bytes(), &job)
		return job.State == "completed"
	})
}

// The server keeps a driver ID owned by its last session until that session's
// heartbeat expires. A replacement worker must wait out the TTL, not exit.
func TestIdleWorkerSessionConflictResolvesAfterServerTTL(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the server's real driver heartbeat TTL")
	}
	t.Parallel()
	s := startLiveServer(t, t.TempDir(), "127.0.0.1:0")
	if w := request(t, s.handler, "POST", "/api/v1/drivers/bench/poll", `{"session":"crashed-process"}`, "test-key"); w.Code != 200 {
		t.Fatalf("seed stale session: %d %s", w.Code, w.Body.String())
	}
	logs := &workerLog{}
	startWorker(t, s.url(), "bench", logs)
	eventually(t, 5*time.Second, "a logged session conflict", func() bool { return logs.contains("409") })
	eventually(t, 20*time.Second, "the worker to take over the driver ID after the TTL", func() bool {
		return logs.contains("reconnected")
	})
	if w := request(t, s.handler, "POST", "/api/v1/drivers/bench/poll", `{"session":"crashed-process"}`, "test-key"); w.Code != 409 {
		t.Fatalf("stale session still owns the driver: %d %s", w.Code, w.Body.String())
	}
}
