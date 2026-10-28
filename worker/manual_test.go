package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunExecutesSeparateManualLeaseWithoutJobProgram(t *testing.T) {
	var mu sync.Mutex
	var manualStates []string
	var initialWithoutJob bool
	terminal := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/poll") {
			http.Error(w, "unexpected request", 400)
			return
		}
		state, _ := body["manual_state"].(string)
		mu.Lock()
		defer mu.Unlock()
		if state == "" {
			initialWithoutJob = body["job_id"] == nil && body["gcode"] == nil
			io.WriteString(w, `{"command":"manual","lease":"manual-lease-secret","manual":{"action":"home","laser_off":true,"max_duration_ms":30000}}`)
			return
		}
		manualStates = append(manualStates, state)
		if body["job_id"] != nil || body["gcode"] != nil || body["state"] != nil || body["lease"] != nil {
			t.Errorf("manual report mixed job fields: %#v", body)
		}
		if state == "running" {
			io.WriteString(w, `{"command":"manual_running"}`)
			return
		}
		if state == "completed" {
			close(terminal)
		}
		io.WriteString(w, `{"command":"idle"}`)
	}))
	defer server.Close()

	client, machine := net.Pipe()
	wire := make(chan string, 20)
	go func() {
		r := bufio.NewReader(machine)
		readByte := func() byte { b, _ := r.ReadByte(); wire <- string([]byte{b}); return b }
		readLine := func() string { line, _ := r.ReadString('\n'); wire <- line; return line }
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		io.WriteString(machine, "ok\r\n")
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		io.WriteString(machine, "ok\r\n")
		// Keep the homing state visible long enough for a running lease poll.
		for i := 0; i < 7; i++ {
			readByte()
			io.WriteString(machine, "<Home|MPos:0,0,0>\r\n")
		}
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "bench", func(context.Context) (io.ReadWriteCloser, error) { return client, nil }, Options{Logf: func(string, ...any) {}})
	}()
	select {
	case <-terminal:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("worker did not report terminal manual state")
	}
	mu.Lock()
	states := append([]string(nil), manualStates...)
	noJob := initialWithoutJob
	mu.Unlock()
	if !noJob {
		t.Fatal("manual pickup included a job or G-code field")
	}
	if len(states) < 2 || states[0] != "running" || states[len(states)-1] != "completed" {
		t.Fatalf("manual states = %#v", states)
	}
	want := []string{"?", "M5\n", "?", "$H\n", "?"}
	// Ignore repeated home status queries between command pickup and completion.
	for i, w := range want {
		if i >= len(wire) {
			t.Fatalf("missing wire evidence %q", w)
		}
		got := <-wire
		if got != w {
			t.Fatalf("wire %d = %q; want %q", i+1, got, w)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("worker exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	_ = machine.Close()
}

func TestRunManualCancelStopsJogWithRealtimeByte(t *testing.T) {
	terminal := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["manual_state"] == "running" {
			io.WriteString(w, `{"command":"manual_cancel"}`)
			return
		}
		if body["manual_state"] == "canceled" {
			terminal <- "canceled"
			io.WriteString(w, `{"command":"idle"}`)
			return
		}
		io.WriteString(w, `{"command":"manual","lease":"lease-jog","manual":{"action":"jog","axis":"X","distance_mm":2,"feed_mm_min":300,"laser_off":true,"max_duration_ms":10000}}`)
	}))
	defer server.Close()
	client, machine := net.Pipe()
	wire := make(chan string, 32)
	go func() {
		r := bufio.NewReader(machine)
		readByte := func() byte { b, _ := r.ReadByte(); wire <- string([]byte{b}); return b }
		readLine := func() string { line, _ := r.ReadString('\n'); wire <- line; return line }
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		io.WriteString(machine, "ok\r\n")
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		io.WriteString(machine, "ok\r\n")
		for {
			if readByte() == 0x85 {
				return
			}
			io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	var connects int
	var connectMu sync.Mutex
	go func() {
		done <- RunWithOptions(ctx, server.URL, "key", "bench", func(context.Context) (io.ReadWriteCloser, error) {
			connectMu.Lock()
			connects++
			number := connects
			connectMu.Unlock()
			if number == 1 {
				return client, nil
			}
			statusClient, statusMachine := net.Pipe()
			go func() {
				bufio.NewReader(statusMachine).ReadByte()
				io.WriteString(statusMachine, "<Idle|MPos:0,0,0>\r\n")
			}()
			return statusClient, nil
		}, Options{Logf: func(string, ...any) {}})
	}()
	select {
	case state := <-terminal:
		if state != "canceled" {
			t.Fatalf("terminal state %q", state)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("manual cancellation did not report terminal state")
	}
	var sawCancel bool
	deadline := time.After(time.Second)
	for !sawCancel {
		select {
		case got := <-wire:
			sawCancel = got == string([]byte{0x85})
		case <-deadline:
			t.Fatal("manual cancel did not send realtime 0x85")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("worker exit = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}
