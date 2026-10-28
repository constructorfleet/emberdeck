package worker

import (
	"bufio"
	"context"
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

func TestRunFailsClosedOnPollError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "duplicate", http.StatusConflict) }))
	defer server.Close()
	called := false
	err := Run(context.Background(), server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { called = true; return driver.NewSimulator(), nil })
	if err == nil || called {
		t.Fatalf("err=%v connector called=%v", err, called)
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
		if len(caps) != 1 || caps[0] != "pause_resume" {
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
			if _, e = peer.Read(b); e != nil || string(b) != "!" {
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
