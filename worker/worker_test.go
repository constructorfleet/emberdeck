package worker

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
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
	if err != nil && err != context.DeadlineExceeded {
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
	ctx, cancel := context.WithTimeout(context.Background(), 1800*time.Millisecond)
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
			_, _ = peer.Read(reader) // cancellation realtime bytes
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
