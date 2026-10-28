package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
)

const profileSnapshot = `{"$30":1000,"$31":0,"$32":1}`

func TestControllerFreshStatusIsReportedOnce(t *testing.T) {
	var report controllerReport
	report.setParserResetFresh("Idle")
	first := report.poll("session", nil)
	if first.ControllerState != "Idle" || !first.ControllerStatusFresh || !first.ControllerParserResetFresh {
		t.Fatalf("fresh reset status poll = %#v; want parser-reset-confirmed Idle", first)
	}
	second := report.poll("session", nil)
	if second.ControllerStatusFresh || second.ControllerParserResetFresh {
		t.Fatalf("fresh reset markers were repeated: %#v", second)
	}
}

func TestWorkerPerformsAndReportsRequestedParserReset(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var polls int
	secondReport := make(chan pollRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var report pollRequest
		if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
			t.Errorf("decode poll: %v", err)
			return
		}
		polls++
		if polls == 1 {
			io.WriteString(w, `{"command":"idle","read_parser_reset":true}`)
			return
		}
		secondReport <- report
		io.WriteString(w, `{"command":"idle"}`)
		cancel()
	}))
	defer server.Close()
	wire := make(chan byte, 2)
	connect := func(context.Context) (io.ReadWriteCloser, error) {
		client, machine := net.Pipe()
		go func() {
			defer machine.Close()
			r := bufio.NewReader(machine)
			b, _ := r.ReadByte()
			wire <- b
			io.WriteString(machine, "Grbl 1.1h ['$' for help]\r\n")
			b, _ = r.ReadByte()
			wire <- b
			io.WriteString(machine, "<Alarm|MPos:0,0,0>\r\n")
		}()
		return client, nil
	}
	done := make(chan error, 1)
	go func() { done <- RunWithOptions(ctx, server.URL, "key", "laser", connect, fastOptions(nil)) }()
	select {
	case report := <-secondReport:
		if !report.ControllerStatusFresh || !report.ControllerParserResetFresh || report.ControllerState != "Alarm" {
			t.Fatalf("reset proof poll = %#v; want fresh Alarm parser-reset confirmation", report)
		}
	case <-ctx.Done():
		t.Fatal("worker did not report parser reset confirmation")
	}
	if got := <-wire; got != 0x18 {
		t.Fatalf("first controller byte = 0x%02x; want Ctrl-X", got)
	}
	if got := <-wire; got != '?' {
		t.Fatalf("post-reset query = 0x%02x; want ?", got)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v; want context cancellation", err)
	}
}

// settingsController answers status queries with Idle, `$$` with reply, and
// program lines with ok. It records every byte the worker writes.
func settingsController(reply string, written *strings.Builder, mu *sync.Mutex) io.ReadWriteCloser {
	client, peer := net.Pipe()
	go func() {
		defer peer.Close()
		reader := bufio.NewReader(peer)
		var line strings.Builder
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			mu.Lock()
			written.WriteByte(b)
			mu.Unlock()
			switch b {
			case '?':
				io.WriteString(peer, "<Idle|MPos:0,0,0|WCO:0,0,0>\r\n")
			case '\n':
				text := line.String()
				line.Reset()
				if text == "$$" {
					io.WriteString(peer, reply)
				} else {
					io.WriteString(peer, "ok\r\n")
				}
			default:
				line.WriteByte(b)
			}
		}
	}()
	return client
}

type settingsServer struct {
	mu        sync.Mutex
	assigned  bool
	assign    string
	idle      string
	terminal  map[string]any
	reports   []map[string]any
	terminals chan map[string]any
}

func (s *settingsServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.reports = append(s.reports, body)
		state, _ := body["state"].(string)
		if state == "failed" || state == "completed" || state == "canceled" {
			if s.terminal == nil {
				s.terminal = body
				s.terminals <- body
			}
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		if state != "" {
			io.WriteString(w, s.assign)
			return
		}
		if !s.assigned && s.assign != "" {
			s.assigned = true
			io.WriteString(w, s.assign)
			return
		}
		if s.idle != "" {
			io.WriteString(w, s.idle)
			s.idle = ""
			return
		}
		io.WriteString(w, `{"command":"idle"}`)
	}
}

func runUntilTerminal(t *testing.T, s *settingsServer, connect Connector) map[string]any {
	t.Helper()
	s.terminals = make(chan map[string]any, 1)
	server := httptest.NewServer(s.handler(t))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- RunWithOptions(ctx, server.URL, "key", "laser", connect, fastOptions(nil)) }()
	select {
	case terminal := <-s.terminals:
		cancel()
		<-done
		return terminal
	case err := <-done:
		t.Fatalf("worker stopped before a terminal report: %v", err)
	case <-ctx.Done():
		t.Fatal("no terminal report")
	}
	return nil
}

func TestWorkerRefusesProfileJobWhenControllerSettingsDiffer(t *testing.T) {
	var mu sync.Mutex
	var written strings.Builder
	s := &settingsServer{assign: `{"command":"run","lease":"lease-1","job":{"id":"job-1","gcode":"G0 X1\nM5","expected_settings":` + profileSnapshot + `}}`}
	terminal := runUntilTerminal(t, s, func(context.Context) (io.ReadWriteCloser, error) {
		return settingsController("$20=1\r\n$30=255\r\n$31=0\r\n$32=0\r\n$130=400\r\n$131=300\r\nok\r\n", &written, &mu), nil
	})
	if terminal["state"] != "failed" {
		t.Fatalf("terminal = %v", terminal)
	}
	message, _ := terminal["error"].(string)
	for _, field := range []string{"$30", "controller 255, profile 1000", "$32", "controller 0, profile 1"} {
		if !strings.Contains(message, field) {
			t.Fatalf("terminal error %q lacks %q", message, field)
		}
	}
	reported, _ := terminal["controller_settings"].(map[string]any)
	if reported["$30"] != 255.0 || reported["$32"] != 0.0 || reported["$20"] != 1.0 || reported["$130"] != 400.0 || reported["$131"] != 300.0 {
		t.Fatalf("terminal controller_settings = %v", terminal["controller_settings"])
	}
	mu.Lock()
	defer mu.Unlock()
	if sent := strings.ReplaceAll(written.String(), "?", ""); sent != "$$\n" {
		t.Fatalf("controller received %q; want only $$ before refusing", sent)
	}
}

func TestWorkerStreamsProfileJobWhenControllerSettingsMatch(t *testing.T) {
	var mu sync.Mutex
	var written strings.Builder
	s := &settingsServer{assign: `{"command":"run","lease":"lease-1","job":{"id":"job-1","gcode":"G0 X1\nM5","expected_settings":` + profileSnapshot + `}}`}
	terminal := runUntilTerminal(t, s, func(context.Context) (io.ReadWriteCloser, error) {
		return settingsController("$20=0\r\n$30=1000.000\r\n$31=0.000\r\n$32=1\r\n$130=300.000\r\n$131=200.000\r\nok\r\n", &written, &mu), nil
	})
	if terminal["state"] != "completed" {
		t.Fatalf("terminal = %v", terminal)
	}
	reported, _ := terminal["controller_settings"].(map[string]any)
	if reported["$30"] != 1000.0 || reported["$32"] != 1.0 {
		t.Fatalf("controller_settings = %v", terminal["controller_settings"])
	}
	mu.Lock()
	defer mu.Unlock()
	sent := strings.ReplaceAll(written.String(), "?", "")
	if !strings.HasPrefix(sent, "$$\nG0 X1\n") {
		t.Fatalf("controller received %q; want $$ before the first program line", sent)
	}
}

func TestWorkerReadsSettingsForUploadedJobWithoutComparing(t *testing.T) {
	var mu sync.Mutex
	var written strings.Builder
	s := &settingsServer{assign: `{"command":"run","lease":"lease-1","job":{"id":"job-1","gcode":"G0 X1\nM5"}}`}
	terminal := runUntilTerminal(t, s, func(context.Context) (io.ReadWriteCloser, error) {
		return settingsController("$30=255\r\n$31=0\r\n$32=0\r\nok\r\n", &written, &mu), nil
	})
	if terminal["state"] != "completed" {
		t.Fatalf("terminal = %v", terminal)
	}
	reported, _ := terminal["controller_settings"].(map[string]any)
	if reported["$32"] != 0.0 {
		t.Fatalf("controller_settings = %v", terminal["controller_settings"])
	}
}

func TestIdleWorkerReadsSettingsWhenServerRequestsThem(t *testing.T) {
	var mu sync.Mutex
	connects := 0
	reported := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := pollSession(t, r)
		mu.Lock()
		defer mu.Unlock()
		if settings, ok := body["controller_settings"].(map[string]any); ok {
			select {
			case reported <- settings:
			default:
			}
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		io.WriteString(w, `{"command":"idle","read_settings":true}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
			mu.Lock()
			connects++
			mu.Unlock()
			return driver.NewSimulator(), nil
		}, fastOptions(nil))
	}()
	select {
	case settings := <-reported:
		if settings["$30"] != 1000.0 || settings["$31"] != 0.0 || settings["$32"] != 1.0 {
			t.Fatalf("reported settings = %v", settings)
		}
	case <-ctx.Done():
		t.Fatal("idle worker never reported controller settings")
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if connects != 1 {
		t.Fatalf("settings probe connections = %d", connects)
	}
}

func TestIdleWorkerDoesNotProbeUnlessAsked(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"command":"idle"}`) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	called := false
	_ = RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) { called = true; return driver.NewSimulator(), nil }, fastOptions(nil))
	if called {
		t.Fatal("idle worker opened the controller without a settings request")
	}
}

func TestSettingsProbeResetRefusesNextJobWithoutConnecting(t *testing.T) {
	var mu sync.Mutex
	connects, polls := 0, 0
	resetReported := false
	terminals := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := pollSession(t, r)
		mu.Lock()
		defer mu.Unlock()
		polls++
		if message, _ := body["settings_error"].(string); strings.Contains(message, "reset") {
			resetReported = true
		}
		switch state, _ := body["state"].(string); {
		case state == "failed" || state == "completed":
			select {
			case terminals <- body:
			default:
			}
			io.WriteString(w, `{"command":"idle"}`)
		case polls == 1:
			io.WriteString(w, `{"command":"idle","read_settings":true}`)
		case resetReported:
			io.WriteString(w, `{"command":"run","lease":"lease-1","job":{"id":"job-1","gcode":"G0 X1\nM5"}}`)
		default:
			io.WriteString(w, `{"command":"idle"}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "laser", func(context.Context) (io.ReadWriteCloser, error) {
			mu.Lock()
			connects++
			mu.Unlock()
			client, peer := net.Pipe()
			go func() {
				reader := bufio.NewReader(peer)
				reader.ReadString('\n')
				io.WriteString(peer, "Grbl 1.1h ['$' for help]\r\n")
			}()
			return client, nil
		}, fastOptions(nil))
	}()
	var terminal map[string]any
	select {
	case terminal = <-terminals:
	case <-ctx.Done():
		t.Fatal("no terminal report after probe reset")
	}
	cancel()
	<-done
	if terminal["state"] != "failed" || !strings.Contains(terminal["error"].(string), "reset") {
		t.Fatalf("terminal = %v", terminal)
	}
	mu.Lock()
	defer mu.Unlock()
	if connects != 1 {
		t.Fatalf("connections = %d; the refused job must not reopen the controller", connects)
	}
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
