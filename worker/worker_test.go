package worker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/jobtransfer"
)

func TestRunRetriesTerminalAndNeverResendsCompletedLease(t *testing.T) {
	var mu sync.Mutex
	polls, reports, connects := 0, 0, 0
	sessions := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/drivers/laser/poll" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		session, _ := body["session"].(string)
		if session == "" {
			t.Error("missing session")
		}
		mu.Lock()
		defer mu.Unlock()
		sessions[session] = true
		if body["state"] == "completed" {
			reports++
			if reports == 1 {
				http.Error(w, "transient", http.StatusServiceUnavailable)
				return
			}
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		polls++
		if polls == 1 || polls == 2 {
			io.WriteString(w, `{"command":"run","lease":"lease-1","job":{"id":"job-1","gcode":"M5"}}`)
			return
		}
		io.WriteString(w, `{"command":"idle"}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3200*time.Millisecond)
	defer cancel()
	err := Run(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
		mu.Lock()
		connects++
		mu.Unlock()
		return driver.NewSimulator(), nil
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if reports != 3 {
		t.Fatalf("terminal report attempts = %d", reports)
	}
	if connects != 1 {
		t.Fatalf("controller connections = %d", connects)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %v", sessions)
	}
}

func TestRunFetchesAndVerifiesChunkedContentBeforeOpeningController(t *testing.T) {
	program := strings.Repeat("(c)\n", 17000) + "M5\n"
	sum := sha256.Sum256([]byte(program))
	digest := hex.EncodeToString(sum[:])
	jobID, lease := strings.Repeat("a", 32), strings.Repeat("b", 64)
	var mu sync.Mutex
	ranges := make(map[string]int)
	session := ""
	terminal := make(chan struct{})
	var terminalOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.URL.Path != "/api/v1/drivers/laser/jobs/"+jobID+"/content" || r.Header.Get("X-EmberDeck-Lease") != lease {
				t.Errorf("unexpected content request: %s %s, lease=%q", r.Method, r.URL.Path, r.Header.Get("X-EmberDeck-Lease"))
			}
			mu.Lock()
			if session == "" {
				session = r.Header.Get("X-EmberDeck-Session")
			}
			if r.Header.Get("X-EmberDeck-Session") != session || session == "" {
				t.Errorf("content session = %q, poll session = %q", r.Header.Get("X-EmberDeck-Session"), session)
			}
			requestRange := r.Header.Get("Range")
			ranges[requestRange]++
			attempt := ranges[requestRange]
			mu.Unlock()
			if requestRange == "bytes=0-65535" && attempt == 1 {
				http.Error(w, "temporary lost chunk response", http.StatusServiceUnavailable)
				return
			}
			var start, end int
			if _, err := fmt.Sscanf(requestRange, "bytes=%d-%d", &start, &end); err != nil || start < 0 || end >= len(program) || start > end {
				t.Errorf("invalid range request %q", requestRange)
				http.Error(w, "invalid range", http.StatusRequestedRangeNotSatisfiable)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(program)))
			w.Header().Set("X-EmberDeck-Content-SHA256", digest)
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, program[start:end+1])
			return
		}
		body := pollSession(t, r)
		caps, _ := body["capabilities"].([]any)
		if len(caps) != 2 || caps[1] != jobtransfer.Capability {
			t.Errorf("worker capabilities = %#v", body["capabilities"])
		}
		if body["state"] == "completed" {
			mu.Lock()
			if session != body["session"] {
				t.Errorf("poll session = %#v; content session = %q", body["session"], session)
			}
			mu.Unlock()
			terminalOnce.Do(func() { close(terminal) })
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		mu.Lock()
		session = body["session"].(string)
		mu.Unlock()
		io.WriteString(w, fmt.Sprintf(`{"command":"run","protocol":2,"lease":%q,"job":{"id":%q,"size":%d,"content":{"size":%d,"sha256":%q}}}`, lease, jobID, len(program), len(program), digest))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
			mu.Lock()
			defer mu.Unlock()
			if ranges["bytes=0-65535"] != 2 || ranges[fmt.Sprintf("bytes=65536-%d", len(program)-1)] != 1 {
				t.Errorf("controller opened before complete verified download: %v", ranges)
			}
			return driver.NewSimulator(), nil
		}, fastOptions(nil))
	}()
	select {
	case <-terminal:
	case <-time.After(8 * time.Second):
		cancel()
		t.Fatal("worker did not finish the verified chunked assignment")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if ranges["bytes=0-65535"] != 2 || ranges[fmt.Sprintf("bytes=65536-%d", len(program)-1)] != 1 {
		t.Fatalf("chunk requests = %v", ranges)
	}
}

func TestRunRejectsUnverifiedOrInvalidChunkedProgramBeforeControllerOutput(t *testing.T) {
	valid := []byte("M5\n")
	validSum := sha256.Sum256(valid)
	tests := []struct {
		name           string
		manifest       []byte
		response       []byte
		digest         string
		wantChunk      int
		rangeHeader    string
		responseDigest string
	}{
		{name: "truncated", manifest: valid, response: []byte("M5"), digest: hex.EncodeToString(validSum[:]), wantChunk: 3},
		{name: "altered digest", manifest: valid, response: []byte("G0\n"), digest: hex.EncodeToString(validSum[:]), wantChunk: 3},
		{name: "invalid content range", manifest: valid, response: valid, digest: hex.EncodeToString(validSum[:]), wantChunk: 3, rangeHeader: "bytes 1-3/3"},
		{name: "invalid digest header", manifest: valid, response: valid, digest: hex.EncodeToString(validSum[:]), wantChunk: 3, responseDigest: strings.Repeat("0", 64)},
		{name: "invalid gcode", manifest: []byte("M5!\n"), response: []byte("M5!\n"), wantChunk: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.digest == "" {
				sum := sha256.Sum256(test.manifest)
				test.digest = hex.EncodeToString(sum[:])
			}
			jobID, lease := strings.Repeat("c", 32), strings.Repeat("d", 64)
			failed := make(chan struct{})
			var failedOnce sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/octet-stream")
					contentRange := fmt.Sprintf("bytes 0-%d/%d", test.wantChunk-1, len(test.manifest))
					if test.rangeHeader != "" {
						contentRange = test.rangeHeader
					}
					w.Header().Set("Content-Range", contentRange)
					responseDigest := test.digest
					if test.responseDigest != "" {
						responseDigest = test.responseDigest
					}
					w.Header().Set("X-EmberDeck-Content-SHA256", responseDigest)
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(test.response)
					return
				}
				body := pollSession(t, r)
				if body["state"] == "failed" {
					if body["error"] == "" {
						t.Error("failed transfer receipt has no reason")
					}
					failedOnce.Do(func() { close(failed) })
					io.WriteString(w, `{"command":"idle"}`)
					return
				}
				sum := sha256.Sum256(test.manifest)
				digest := test.digest
				if test.name == "invalid gcode" {
					digest = hex.EncodeToString(sum[:])
				}
				io.WriteString(w, fmt.Sprintf(`{"command":"run","protocol":2,"lease":%q,"job":{"id":%q,"size":%d,"content":{"size":%d,"sha256":%q}}}`, lease, jobID, len(test.manifest), len(test.manifest), digest))
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			connected := false
			go func() {
				done <- RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
					connected = true
					return driver.NewSimulator(), nil
				}, fastOptions(nil))
			}()
			select {
			case <-failed:
			case <-time.After(5 * time.Second):
				cancel()
				t.Fatal("worker did not report invalid chunked content")
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("worker exit = %v", err)
			}
			if connected {
				t.Fatal("worker connected to controller before content verification and G-code validation")
			}
		})
	}
}

func TestRunRejectsBadCredentialsWithoutConnecting(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unauthorized", http.StatusUnauthorized) }))
	defer server.Close()
	called := false
	err := RunWithOptions(context.Background(), server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { called = true; return driver.NewSimulator(), nil }, fastOptions(nil))
	if err == nil || called {
		t.Fatalf("err=%v connector called=%v", err, called)
	}
}

func fastOptions(logs *logRecorder) Options {
	options := Options{RetryMin: 5 * time.Millisecond, RetryMax: 20 * time.Millisecond}
	if logs != nil {
		options.Logf = logs.Logf
	}
	return options
}

type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (l *logRecorder) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logRecorder) matching(fragment string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var result []string
	for _, line := range l.lines {
		if strings.Contains(line, fragment) {
			result = append(result, line)
		}
	}
	return result
}

func pollSession(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	return body
}

func TestIdleWorkerRetriesPollErrorsWithFreshSessionsAndLogsEachAttempt(t *testing.T) {
	var mu sync.Mutex
	sessions := []string{}
	recovered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := pollSession(t, r)
		mu.Lock()
		defer mu.Unlock()
		sessions = append(sessions, body["session"].(string))
		switch len(sessions) {
		case 1, 2, 3:
			http.Error(w, "upgrading", http.StatusBadGateway)
		case 4:
			close(recovered)
			io.WriteString(w, `{"command":"idle"}`)
		default:
			io.WriteString(w, `{"command":"idle"}`)
		}
	}))
	defer server.Close()
	logs := &logRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
			t.Error("idle worker opened the controller")
			return driver.NewSimulator(), nil
		}, fastOptions(logs))
	}()
	select {
	case <-recovered:
	case err := <-done:
		t.Fatalf("idle worker exited after a poll error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("idle worker did not recover")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	unique := map[string]bool{}
	for _, session := range sessions[:4] {
		unique[session] = true
	}
	if len(unique) != 4 {
		t.Fatalf("each retry must use a fresh session: %v", sessions)
	}
	if attempts := logs.matching("retry"); len(attempts) != 3 || !strings.Contains(attempts[0], "502") {
		t.Fatalf("retry logs = %q", logs.lines)
	}
}

func TestIdleWorkerSessionConflictResolvesAfterTTL(t *testing.T) {
	const ttl = 150 * time.Millisecond
	var mu sync.Mutex
	owner, lastSeen := "previous-process", time.Now()
	conflicts := 0
	registered := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session := pollSession(t, r)["session"].(string)
		mu.Lock()
		defer mu.Unlock()
		if time.Since(lastSeen) > ttl {
			owner = ""
		}
		if owner != "" && owner != session {
			conflicts++
			http.Error(w, "driver ID already owned by another session", http.StatusConflict)
			return
		}
		if owner == "" {
			select {
			case registered <- session:
			default:
			}
		}
		owner, lastSeen = session, time.Now()
		io.WriteString(w, `{"command":"idle"}`)
	}))
	defer server.Close()
	logs := &logRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { return driver.NewSimulator(), nil }, fastOptions(logs))
	}()
	select {
	case session := <-registered:
		if session == "previous-process" {
			t.Fatal("worker reused a foreign session")
		}
	case err := <-done:
		t.Fatalf("worker exited on session conflict: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("session conflict did not resolve")
	}
	mu.Lock()
	defer mu.Unlock()
	if conflicts == 0 || len(logs.matching("409")) != conflicts {
		t.Fatalf("conflicts=%d logs=%q", conflicts, logs.lines)
	}
}

// holdingController answers status queries but never acknowledges a program
// line, so the assignment stays active until the worker closes the wire.
func holdingController(closed chan<- struct{}) io.ReadWriteCloser {
	client, peer := net.Pipe()
	go func() {
		defer close(closed)
		defer peer.Close()
		buffer := make([]byte, 64)
		state := "Idle" // preflight requires Idle before program bytes
		for {
			n, err := peer.Read(buffer)
			if err != nil {
				return
			}
			if strings.Contains(string(buffer[:n]), "?") {
				io.WriteString(peer, "<"+state+"|MPos:0,0,0>\n")
				state = "Run"
			}
		}
	}()
	return client
}

func TestWorkerWithActiveLeaseFailsClosedOnReportError(t *testing.T) {
	var mu sync.Mutex
	pollsAfterFailure, failed := 0, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := pollSession(t, r)
		mu.Lock()
		defer mu.Unlock()
		if failed {
			pollsAfterFailure++
		}
		if body["state"] == "running" {
			failed = true
			http.Error(w, "proxy restarting", http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"command":"run","lease":"lease-a","job":{"id":"job-a","gcode":"G1 X1\nG1 X2"}}`)
	}))
	defer server.Close()
	closed := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { return holdingController(closed), nil }, fastOptions(nil))
	if err == nil || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "progress report") {
		t.Fatalf("active lease must fail closed, got %v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("controller connection left open after fail-closed exit")
	}
	mu.Lock()
	defer mu.Unlock()
	if pollsAfterFailure != 0 {
		t.Fatalf("worker sent %d requests after failing closed", pollsAfterFailure)
	}
}

func TestSuperviseRestartsWithFreshSessionAfterFailClosedExit(t *testing.T) {
	var mu sync.Mutex
	sessions := map[string]bool{}
	assigned := false
	restarted := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := pollSession(t, r)
		mu.Lock()
		defer mu.Unlock()
		sessions[body["session"].(string)] = true
		if body["state"] == "running" {
			http.Error(w, "proxy restarting", http.StatusBadGateway)
			return
		}
		if !assigned {
			assigned = true
			io.WriteString(w, `{"command":"run","lease":"lease-s","job":{"id":"job-s","gcode":"G1 X1\nG1 X2"}}`)
			return
		}
		if len(sessions) >= 2 {
			once.Do(func() { close(restarted) })
		}
		io.WriteString(w, `{"command":"idle"}`)
	}))
	defer server.Close()
	logs := &logRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Supervise(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { return holdingController(make(chan struct{})), nil }, fastOptions(logs))
	}()
	select {
	case <-restarted:
	case err := <-done:
		t.Fatalf("supervisor exited: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("worker was not restarted; logs=%q", logs.lines)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("supervisor exit = %v", err)
	}
	if len(logs.matching("progress report")) == 0 {
		t.Fatalf("failure was not logged: %q", logs.lines)
	}
}

func TestRunReportsPositionCorrelatedPathProgressIndependentOfAckCount(t *testing.T) {
	positionReported := make(chan struct{}, 1)
	terminalReported := make(chan map[string]any, 1)
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body["state"] == "completed" {
			terminalReported <- body
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		if execution, ok := body["execution"].(map[string]any); ok {
			path, _ := execution["path_progress"].(map[string]any)
			position, _ := execution["work_position"].(map[string]any)
			if body["acked"] == float64(4) && position["x"] == float64(5) && path["completed"] == float64(0) && path["active"] == float64(0) && path["confidence"] == "estimated" {
				once.Do(func() { positionReported <- struct{}{} })
			}
		}
		if body["job_id"] == nil {
			io.WriteString(w, `{"command":"run","lease":"lease-path","job":{"id":"job-path","gcode":"G21 G90\nG0 X0 Y0\nM4 S100\nG1 X10 Y0 F300"}}`)
			return
		}
		io.WriteString(w, `{"command":"run","lease":"lease-path","job":{"id":"job-path"}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	var phase atomic.Int32
	err := Run(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
		client, controller := net.Pipe()
		go func() {
			defer controller.Close()
			reader := bufio.NewReader(controller)
			var line []byte
			for {
				b, err := reader.ReadByte()
				if err != nil {
					return
				}
				if b == '?' {
					if phase.Load() == 0 {
						io.WriteString(controller, "<Idle|MPos:0,0,0|WCO:0,0,0>\r\n")
					} else if phase.Load() == 1 {
						io.WriteString(controller, "<Run|MPos:5,0,0>\r\n")
						select {
						case <-positionReported:
							phase.Store(2)
						default:
						}
					} else {
						io.WriteString(controller, "<Idle|MPos:10,0,0>\r\n")
					}
					continue
				}
				if b == 0x18 {
					return
				}
				if b != '\n' {
					line = append(line, b)
					continue
				}
				command := string(append(line, b))
				line = nil
				if strings.HasPrefix(command, "G1 X10") {
					phase.Store(1)
				}
				if _, err := io.WriteString(controller, "ok\r\n"); err != nil {
					return
				}
			}
		}()
		return client, nil
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case report := <-terminalReported:
		execution, _ := report["execution"].(map[string]any)
		path, _ := execution["path_progress"].(map[string]any)
		if path["confidence"] != "controller_idle" || path["completed"] != float64(1) {
			t.Fatalf("terminal execution = %#v", report)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not report terminal completion")
	}
}

func TestRunStopsStreamWhenServerRequestsCancel(t *testing.T) {
	var mu sync.Mutex
	var terminal map[string]any
	issued, canceled := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		switch body["state"] {
		case "running":
			io.WriteString(w, `{"command":"cancel","lease":"lease-c","job":{"id":"job-c"}}`)
		case "canceled":
			terminal = body
			canceled = true
			io.WriteString(w, `{"command":"idle"}`)
		default:
			if !issued && !canceled {
				issued = true
				io.WriteString(w, `{"command":"run","lease":"lease-c","job":{"id":"job-c","gcode":"G1 X1\nG1 X2"}}`)
			} else {
				io.WriteString(w, `{"command":"idle"}`)
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := Run(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
		client, peer := net.Pipe()
		go func() {
			defer peer.Close()
			reader := make([]byte, 64)
			n, e := peer.Read(reader)
			if e != nil || string(reader[:n]) != "?" {
				return
			}
			io.WriteString(peer, "<Idle|MPos:0,0,0>\n")
			if n, e = peer.Read(reader); e != nil || string(reader[:n]) != "$$\n" {
				return
			}
			io.WriteString(peer, driver.SimulatorSettings)
			n, e = peer.Read(reader)
			if e != nil {
				return
			}
			if string(reader[:n]) != "G1 X1\n" {
				return
			}
			io.WriteString(peer, "ok\n")
			if _, e = peer.Read(reader); e != nil {
				return
			} // accept second command but hold its ack
			for {
				n, e = peer.Read(reader)
				if e != nil {
					return
				}
				if string(reader[:n]) == "?" {
					io.WriteString(peer, "<Run|MPos:0,0,0>\n")
					continue
				}
				if string(reader[:n]) == "!\x18" {
					return
				}
			}
		}()
		return client, nil
	})
	if err != nil && err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if terminal == nil || terminal["state"] != "canceled" {
		t.Fatalf("terminal report = %#v", terminal)
	}
	if terminal["acked"] != float64(1) || terminal["total"] != float64(2) {
		t.Fatalf("canceled progress = %#v", terminal)
	}
}

func TestRunPreflightsProgramBeforeOpeningController(t *testing.T) {
	var failed bool
	issued := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		switch body["state"] {
		case "failed":
			failed = body["error"] != ""
			io.WriteString(w, `{"command":"idle"}`)
		default:
			if !issued {
				issued = true
				io.WriteString(w, `{"command":"run","lease":"lease-invalid","job":{"id":"job-invalid","gcode":"G0 X1 ?"}}`)
			} else {
				io.WriteString(w, `{"command":"idle"}`)
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	called := false
	err := Run(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { called = true; return driver.NewSimulator(), nil })
	if err != nil && err != context.DeadlineExceeded {
		t.Fatal(err)
	}
	if called || !failed {
		t.Fatalf("connector called=%v failed report=%v", called, failed)
	}
}

func TestRunInitialPauseSendsNoProgramUntilResumeAndReportsController(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	pausedReports := 0
	paused, completed := false, false
	sawPausing := false
	var terminal map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		caps, _ := body["capabilities"].([]any)
		if len(caps) != 2 || caps[0] != "pause_resume" || caps[1] != jobtransfer.Capability {
			t.Errorf("capabilities = %#v", body["capabilities"])
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		if body["state"] == "completed" || body["state"] == "failed" || body["state"] == "canceled" {
			terminal = body
			completed = body["state"] == "completed"
			if body["state"] == "completed" && body["controller_state"] != "Idle" {
				t.Errorf("completed controller state = %#v", body["controller_state"])
			}
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		if body["state"] == "paused" {
			if body["controller_state"] != "Hold:0" {
				t.Errorf("paused controller state = %#v", body["controller_state"])
			}
			paused = true
			pausedReports++
			if pausedReports == 1 {
				io.WriteString(w, `{"command":"pause","lease":"lease-p","job":{"id":"job-p"}}`)
			} else {
				io.WriteString(w, `{"command":"resume","lease":"lease-p","job":{"id":"job-p"}}`)
			}
			return
		}
		if body["state"] == "pausing" {
			if body["controller_state"] != "Hold:1" {
				t.Errorf("pausing controller state = %#v", body["controller_state"])
			}
			sawPausing = true
			io.WriteString(w, `{"command":"pause","lease":"lease-p","job":{"id":"job-p"}}`)
			return
		}
		if body["state"] == "resuming" {
			io.WriteString(w, `{"command":"resume","lease":"lease-p","job":{"id":"job-p"}}`)
			return
		}
		if requests == 1 {
			io.WriteString(w, `{"command":"pause","lease":"lease-p","job":{"id":"job-p","gcode":"G1 X1"}}`)
			return
		}
		io.WriteString(w, `{"command":"run","lease":"lease-p","job":{"id":"job-p"}}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	err := Run(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
		client, peer := net.Pipe()
		go func() {
			defer peer.Close()
			r := bufio.NewReader(peer)
			b := make([]byte, 1)
			_, e := peer.Read(b)
			if e != nil || string(b) != "?" {
				done <- fmt.Errorf("initial query %q: %v", b, e)
				return
			}
			io.WriteString(peer, "<Idle|MPos:0,0,0>\r\n")
			settingsQuery := make([]byte, 3)
			if _, e = io.ReadFull(peer, settingsQuery); e != nil || string(settingsQuery) != "$$\n" {
				done <- fmt.Errorf("settings query %q: %v", settingsQuery, e)
				return
			}
			io.WriteString(peer, driver.SimulatorSettings)
			for {
				if _, e = peer.Read(b); e != nil || string(b) != "?" {
					break
				}
				io.WriteString(peer, "<Idle|MPos:0,0,0>\r\n")
			}
			if e != nil || string(b) != "!" {
				done <- fmt.Errorf("feed hold %q: %v", b, e)
				return
			}
			if _, e = peer.Read(b); e != nil || string(b) != "?" {
				done <- fmt.Errorf("hold query %q: %v", b, e)
				return
			}
			io.WriteString(peer, "<Hold:1|MPos:0,0,0>\r\n")
			pauseStarted := time.Now()
			for {
				if _, e = peer.Read(b); e != nil {
					done <- fmt.Errorf("resume: %v", e)
					return
				}
				if string(b) == "~" {
					break
				}
				if string(b) == "?" {
					if time.Since(pauseStarted) < 1300*time.Millisecond {
						io.WriteString(peer, "<Hold:1|MPos:0,0,0>\r\n")
					} else {
						io.WriteString(peer, "<Hold:0|MPos:0,0,0>\r\n")
					}
					continue
				}
				done <- fmt.Errorf("unexpected byte while paused: %q", b)
				return
			}
			var first byte
			for {
				first, e = r.ReadByte()
				if e != nil {
					done <- fmt.Errorf("resume status: %v", e)
					return
				}
				if first != '?' {
					break
				}
				io.WriteString(peer, "<Run|MPos:0,0,0>\r\n")
			}
			line, e := r.ReadString('\n')
			line = string(first) + line
			if e != nil || line != "G1 X1\n" {
				done <- fmt.Errorf("source line %q: %v", line, e)
				return
			}
			io.WriteString(peer, "ok\r\n")
			line, e = r.ReadString('\n')
			if e != nil || line != "M5\n" {
				done <- fmt.Errorf("cleanup %q: %v", line, e)
				return
			}
			io.WriteString(peer, "ok\r\n")
			for {
				if _, e = peer.Read(b); e != nil {
					done <- fmt.Errorf("final query: %v", e)
					return
				}
				if string(b) == "?" {
					break
				}
			}
			io.WriteString(peer, "<Idle|MPos:1,0,0>\r\n")
			_, _ = io.Copy(io.Discard, peer)
			done <- nil
		}()
		return client, nil
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller exchange did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawPausing || !paused || pausedReports < 2 || !completed {
		t.Fatalf("pausing=%v paused=%v pausedReports=%d completed=%v terminal=%#v requests=%d", sawPausing, paused, pausedReports, completed, terminal, requests)
	}
}

func TestRunReportsExternalHoldAndWaitsForExplicitResume(t *testing.T) {
	var mu sync.Mutex
	issued, sawPausing, sawPaused, completed := false, false, false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		defer mu.Unlock()
		switch body["state"] {
		case "pausing":
			if body["controller_state"] != "Hold:1" {
				t.Errorf("external hold pending state = %#v", body)
			}
			sawPausing = true
			io.WriteString(w, `{"command":"pause","lease":"lease-e","job":{"id":"job-e"}}`)
		case "paused":
			if body["controller_state"] != "Hold:0" {
				t.Errorf("external hold paused state = %#v", body)
			}
			sawPaused = true
			io.WriteString(w, `{"command":"resume","lease":"lease-e","job":{"id":"job-e"}}`)
		case "resuming":
			io.WriteString(w, `{"command":"resume","lease":"lease-e","job":{"id":"job-e"}}`)
		case "completed":
			completed = true
			io.WriteString(w, `{"command":"idle"}`)
		default:
			if !issued {
				issued = true
				io.WriteString(w, `{"command":"run","lease":"lease-e","job":{"id":"job-e","gcode":"G1 X1\nG1 X2"}}`)
			} else {
				io.WriteString(w, `{"command":"run","lease":"lease-e","job":{"id":"job-e"}}`)
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	peerDone := make(chan error, 1)
	resumeSent := atomic.Bool{}
	err := Run(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
		client, peer := net.Pipe()
		go func() {
			defer peer.Close()
			reader := bufio.NewReader(peer)
			var externalHoldAt time.Time
			controller := "Idle"
			lineCount := 0
			for {
				first, e := reader.ReadByte()
				if e != nil {
					if lineCount == 2 && controller == "Idle" {
						peerDone <- nil
					} else {
						peerDone <- e
					}
					return
				}
				switch first {
				case '?':
					if !externalHoldAt.IsZero() && !resumeSent.Load() {
						if time.Since(externalHoldAt) >= 1300*time.Millisecond {
							controller = "Hold:0"
						} else {
							controller = "Hold:1"
						}
					}
					if _, e = io.WriteString(peer, "<"+controller+"|MPos:0,0,0>\r\n"); e != nil {
						peerDone <- e
						return
					}
				case '!':
					// The server may reaffirm a hold after the worker reports it.
				case '~':
					resumeSent.Store(true)
					controller = "Run"
				default:
					line, e := reader.ReadString('\n')
					line = string(first) + line
					if e != nil {
						peerDone <- e
						return
					}
					switch line {
					case "$$\n":
						io.WriteString(peer, driver.SimulatorSettings)
					case "G1 X1\n":
						lineCount++
						externalHoldAt = time.Now()
						controller = "Hold:1"
						io.WriteString(peer, "<Hold:1|MPos:0,0,0>\r\nok\r\n")
					case "G1 X2\n":
						if !resumeSent.Load() {
							peerDone <- fmt.Errorf("second source line before explicit resume")
							return
						}
						lineCount++
						io.WriteString(peer, "ok\r\n")
					case "M5\n":
						controller = "Idle"
						io.WriteString(peer, "ok\r\n")
					default:
						peerDone <- fmt.Errorf("unexpected controller command %q", line)
						return
					}
				}
			}
		}()
		return client, nil
	})
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	select {
	case err := <-peerDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("controller exchange did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawPausing || !sawPaused || !completed {
		t.Fatalf("pausing=%v paused=%v completed=%v", sawPausing, sawPaused, completed)
	}
}

func TestRunLogsConnectionAndJobOutcome(t *testing.T) {
	var mu sync.Mutex
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := pollSession(t, r)
		mu.Lock()
		defer mu.Unlock()
		if body["state"] != nil {
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		polls++
		if polls == 1 {
			io.WriteString(w, `{"command":"run","lease":"lease-1","job":{"id":"job-1","gcode":"M5"}}`)
			return
		}
		io.WriteString(w, `{"command":"idle"}`)
	}))
	defer server.Close()
	logs := &logRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { return driver.NewSimulator(), nil }, fastOptions(logs))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(logs.matching("job job-1 completed")) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if len(logs.matching("worker laser: connected to "+server.URL)) != 1 {
		t.Errorf("connection not logged once: %v", logs.lines)
	}
	if len(logs.matching("worker laser: running job job-1")) != 1 {
		t.Errorf("job start not logged: %v", logs.lines)
	}
	if len(logs.matching("worker laser: job job-1 completed")) != 1 {
		t.Errorf("job outcome not logged: %v", logs.lines)
	}
}
