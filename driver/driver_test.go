package driver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamSendsLinesAndWaitsForIdle(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	go func() {
		reader := make([]byte, 64)
		n, _ := machine.Read(reader)
		if got := string(reader[:n]); got != "?" {
			t.Errorf("initial status query = %q", got)
		}
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		n, _ = machine.Read(reader)
		if got := string(reader[:n]); got != "G0 X1\n" {
			t.Errorf("first command = %q", got)
		}
		io.WriteString(machine, "ok\r\n")
		n, _ = machine.Read(reader)
		if got := string(reader[:n]); got != "M5\n" {
			t.Errorf("second command = %q", got)
		}
		io.WriteString(machine, "ok\r\n")
		n, _ = machine.Read(reader)
		if got := string(reader[:n]); got != "M5\n" {
			t.Errorf("beam-off cleanup = %q", got)
		}
		io.WriteString(machine, "ok\r\n")
		n, _ = machine.Read(reader)
		if got := string(reader[:n]); got != "?" {
			t.Errorf("status query = %q", got)
		}
		io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
		n, _ = machine.Read(reader)
		if got := string(reader[:n]); got != "?" {
			t.Errorf("second status query = %q", got)
		}
		io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	}()
	var progress []int
	err := Stream(context.Background(), client, "G0 X1\nM5", func(acked, total int) { progress = append(progress, acked) })
	if err != nil {
		t.Fatal(err)
	}
	if len(progress) != 2 || progress[0] != 1 || progress[1] != 2 {
		t.Fatalf("progress = %v", progress)
	}
}

func TestStreamControlledPausesInFlightAndResumesSameProgram(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	done := make(chan error, 1)
	go func() { done <- StreamControlled(context.Background(), client, "G1 X1\nG1 X2", commands, nil, nil) }()
	reader := bufio.NewReader(machine)
	var b [1]byte
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "?" {
		t.Fatalf("initial query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	line, _ := reader.ReadString('\n')
	if line != "G1 X1\n" {
		t.Fatalf("first source line = %q", line)
	}
	commands <- Pause
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "!" {
		t.Fatalf("pause byte = %q, %v", b[:], err)
	}
	io.WriteString(machine, "ok\r\n<Hold:1|MPos:0,0,0>\r\n")
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "?" {
		t.Fatalf("pause status query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
	time.Sleep(30 * time.Millisecond)
	machine.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if n, err := machine.Read(b[:]); err == nil {
		t.Fatalf("unexpected write while pause is pending: %q", b[:n])
	}
	machine.SetReadDeadline(time.Time{})
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "?" {
		t.Fatalf("pending pause status query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Hold:1|MPos:0,0,0>\r\n")
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "?" {
		t.Fatalf("hold status query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Hold:1|MPos:0,0,0>\r\n")
	commands <- Resume
	time.Sleep(30 * time.Millisecond)
	machine.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if n, err := machine.Read(b[:]); err == nil {
		t.Fatalf("unexpected write while Hold:1 is decelerating: %q", b[:n])
	}
	machine.SetReadDeadline(time.Time{})
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "?" {
		t.Fatalf("continued hold status query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Hold:0|MPos:0,0,0>\r\n")
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "~" {
		t.Fatalf("resume byte = %q, %v", b[:], err)
	}
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "?" {
		t.Fatalf("resume status query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
	line, _ = reader.ReadString('\n')
	if line != "G1 X2\n" {
		t.Fatalf("second source line = %q", line)
	}
	io.WriteString(machine, "ok\r\n")
	line, _ = reader.ReadString('\n')
	if line != "M5\n" {
		t.Fatalf("beam-off cleanup = %q", line)
	}
	io.WriteString(machine, "ok\r\n")
	if _, err := machine.Read(b[:]); err != nil || string(b[:]) != "?" {
		t.Fatalf("final status query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStreamControlledCancellationWhilePausedStopsPromptly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	commands := make(chan Command, 1)
	commands <- Pause
	conn := NewSimulator()
	states := make(chan string, 16)
	progress := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- StreamControlled(ctx, conn, "G1 X1\nG1 X2", commands, func(int, int) { progress <- struct{}{} }, func(s string) { states <- s })
	}()
	for {
		select {
		case state := <-states:
			if state == "Hold:0" {
				cancel()
				goto canceled
			}
		case <-time.After(2 * time.Second):
			t.Fatal("controller did not confirm Hold:0")
		}
	}
canceled:
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not stop after cancellation")
	}
	select {
	case <-progress:
		t.Fatal("source progress advanced while initially paused")
	default:
	}
}

func TestStreamControlledFailsClosedOnUnrequestedRunAfterHold(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	done := make(chan error, 1)
	go func() { done <- StreamControlled(context.Background(), client, "G1 X1\nG1 X2", commands, nil, nil) }()
	reader := bufio.NewReader(machine)
	read := func(want string) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadString('\n')
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire = %q, %v; want %q", got, err, want)
		}
	}
	readByte := func(want byte) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire byte = %q, %v; want %q", got, err, want)
		}
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	read("G1 X1\n")
	commands <- Pause
	readByte('!')
	io.WriteString(machine, "ok\r\n")
	readByte('?')
	io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
	readByte('?')
	io.WriteString(machine, "<Hold:0|MPos:0,0,0>\r\n")
	readByte('?')
	io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
	stopBytes := make(chan string, 1)
	go func() {
		b1, e1 := reader.ReadByte()
		b2, e2 := reader.ReadByte()
		if e1 != nil || e2 != nil {
			stopBytes <- ""
			return
		}
		stopBytes <- string([]byte{b1, b2})
	}()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "without explicit Resume") {
		t.Fatalf("unexpected resume error = %v", err)
	}
	if got := <-stopBytes; got != "!\x18" {
		t.Fatalf("failure stop bytes = %q", got)
	}
}

func TestStreamControlledAppliedCallbacksBracketFreshControllerStates(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	commands <- Pause
	events := make(chan string, 16)
	done := make(chan error, 1)
	go func() {
		done <- StreamControlled(context.Background(), client, "G1 X1", commands, nil,
			func(s string) { events <- "state:" + s },
			func(c Command) { events <- "applied:" + string(c) })
	}()
	reader := bufio.NewReader(machine)
	readByte := func(want byte) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire byte = %q, %v; want %q", got, err, want)
		}
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	readByte('!')
	if got := <-events; got != "state:Idle" {
		t.Fatalf("first callback = %q", got)
	}
	if got := <-events; got != "applied:pause" {
		t.Fatalf("pause callback = %q", got)
	}
	readByte('?')
	io.WriteString(machine, "<Hold:0|MPos:0,0,0>\r\n")
	if got := <-events; got != "state:Hold:0" {
		t.Fatalf("hold callback = %q", got)
	}
	commands <- Resume
	readByte('~')
	if got := <-events; got != "applied:resume" {
		t.Fatalf("resume callback = %q", got)
	}
	readByte('?')
	io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
	if got := <-events; got != "state:Run" {
		t.Fatalf("run callback = %q", got)
	}
	line, err := reader.ReadString('\n')
	if err != nil || line != "G1 X1\n" {
		t.Fatalf("source = %q, %v", line, err)
	}
	io.WriteString(machine, "ok\r\n")
	line, err = reader.ReadString('\n')
	if err != nil || line != "M5\n" {
		t.Fatalf("cleanup = %q, %v", line, err)
	}
	io.WriteString(machine, "ok\r\n")
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	if got := <-events; got != "state:Idle" {
		t.Fatalf("final callback = %q", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type failingControlConn struct{ net.Conn }

func (c failingControlConn) Write(p []byte) (int, error) {
	if len(p) > 0 && p[0] == '!' {
		return 0, fmt.Errorf("injected write failure")
	}
	return c.Conn.Write(p)
}

func TestStreamControlledDoesNotReportAppliedWhenRealtimeWriteFails(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	commands <- Pause
	applied := make(chan Command, 1)
	done := make(chan error, 1)
	go func() {
		done <- StreamControlled(context.Background(), failingControlConn{client}, "G1 X1", commands, nil, nil,
			func(c Command) { applied <- c })
	}()
	var b [1]byte
	if _, err := machine.Read(b[:]); err != nil || b[0] != '?' {
		t.Fatalf("initial query = %q, %v", b[:], err)
	}
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	if err := <-done; err == nil {
		t.Fatal("expected feed-hold write error")
	}
	select {
	case cmd := <-applied:
		t.Fatalf("reported unapplied control %q", cmd)
	default:
	}
}

func TestStreamWaitsForFreshIdleAfterCleanupAck(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	done := make(chan error, 1)
	go func() { done <- Stream(context.Background(), client, "G1 X1", nil) }()
	reader := bufio.NewReader(machine)
	readStatus := func() {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != '?' {
			t.Fatalf("status query = %q, %v", got, err)
		}
	}
	readStatus()
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	line, err := reader.ReadString('\n')
	if err != nil || line != "G1 X1\n" {
		t.Fatalf("source = %q, %v", line, err)
	}
	io.WriteString(machine, "ok\r\n")
	line, err = reader.ReadString('\n')
	if err != nil || line != "M5\n" {
		t.Fatalf("cleanup = %q, %v", line, err)
	}
	io.WriteString(machine, "ok\r\n<Idle|MPos:1,0,0>\r\n") // queued pre-final status
	select {
	case err := <-done:
		t.Fatalf("stream completed on pre-final Idle: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	readStatus()
	io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
	select {
	case err := <-done:
		t.Fatalf("stream completed on fresh Run: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	readStatus()
	io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStreamControlledCanResumePauseAfterCleanupAck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	done := make(chan error, 1)
	states := make(chan string, 16)
	go func() {
		done <- StreamControlled(ctx, client, "G1 X1", commands, nil, func(state string) { states <- state })
	}()
	reader := bufio.NewReader(machine)
	readByte := func(want byte) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire byte = %q, want %q (%v)", got, want, err)
		}
	}
	readLine := func(want string) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadString('\n')
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire line = %q, want %q (%v)", got, want, err)
		}
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	waitState := func(want string) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			select {
			case got := <-states:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("controller did not report %s", want)
			}
		}
	}
	waitState("Idle")
	readLine("G1 X1\n")
	io.WriteString(machine, "ok\r\n")
	readLine("M5\n")
	io.WriteString(machine, "ok\r\n")
	readByte('?')
	io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
	waitState("Run")
	commands <- Pause
	readByte('!')
	readByte('?')
	io.WriteString(machine, "<Hold:0|MPos:1,0,0>\r\n")
	waitState("Hold:0")
	commands <- Resume
	var wire strings.Builder
	wireDeadline := time.Now().Add(time.Second)
	for {
		if err := machine.SetReadDeadline(wireDeadline); err != nil {
			t.Fatal(err)
		}
		got, err := reader.ReadByte()
		if err != nil {
			t.Fatalf("wire bytes after Resume = %q; want ~: %v", wire.String(), err)
		}
		wire.WriteByte(got)
		if got == '~' {
			break
		}
		if got != '?' {
			t.Fatalf("wire bytes after Resume = %q; unexpected byte %q", wire.String(), got)
		}
		io.WriteString(machine, "<Hold:0|MPos:1,0,0>\r\n")
	}
	machine.SetReadDeadline(time.Time{})
	for {
		readByte('?')
		io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
		select {
		case state := <-states:
			if state == "Run" {
				goto resumed
			}
		case <-ctx.Done():
			t.Fatalf("stream did not observe fresh Run after Resume: %v", ctx.Err())
		}
	}

resumed:
	select {
	case err := <-done:
		t.Fatalf("stream completed on resumed Run before final Idle: %v", err)
	default:
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	// The loop can have already selected its next poll before it handles this
	// completion status; keep the peer reading so that poll write can finish.
	machine.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	trailing, trailingErr := reader.ReadByte()
	machine.SetReadDeadline(time.Time{})
	if trailingErr == nil && trailing != '?' {
		t.Fatalf("unexpected trailing wire byte after final Idle: %q", trailing)
	}
	if err := <-done; err != nil {
		t.Fatalf("stream after final-phase resume: %v", err)
	}
}

func TestStreamControlledFinalPauseDoesNotCompleteOnIdleBeforeResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	done := make(chan error, 1)
	states := make(chan string, 16)
	go func() {
		done <- StreamControlled(ctx, client, "G1 X1", commands, nil, func(state string) { states <- state })
	}()
	reader := bufio.NewReader(machine)
	readByte := func(want byte) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire byte = %q, want %q (%v)", got, want, err)
		}
	}
	readLine := func(want string) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadString('\n')
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire line = %q, want %q (%v)", got, want, err)
		}
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	waitState := func(want string) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			select {
			case got := <-states:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("controller did not report %s", want)
			}
		}
	}
	waitState("Idle")
	readLine("G1 X1\n")
	io.WriteString(machine, "ok\r\n")
	readLine("M5\n")
	io.WriteString(machine, "ok\r\n")
	readByte('?')
	io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
	waitState("Run")
	commands <- Pause
	readByte('!')
	readByte('?')
	io.WriteString(machine, "<Hold:0|MPos:1,0,0>\r\n")
	waitState("Hold:0")
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	select {
	case err := <-done:
		t.Fatalf("stream completed while paused on Idle, before explicit Resume: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	commands <- Resume
	resumeDeadline := time.Now().Add(time.Second)
	for {
		machine.SetReadDeadline(resumeDeadline)
		got, err := reader.ReadByte()
		if err != nil {
			t.Fatalf("wire bytes after explicit Resume = %q; want ~: %v", got, err)
		}
		if got == '~' {
			machine.SetReadDeadline(time.Time{})
			break
		}
		if got != '?' {
			t.Fatalf("unexpected wire byte while waiting for Resume: %q", got)
		}
		io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	}
	machine.SetReadDeadline(time.Time{})
	for {
		readByte('?')
		io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
		select {
		case state := <-states:
			if state == "Run" {
				goto resumed
			}
		case <-ctx.Done():
			t.Fatalf("stream did not observe fresh Run after Resume: %v", ctx.Err())
		}
	}

resumed:
	select {
	case err := <-done:
		t.Fatalf("stream completed on resumed Run before final Idle: %v", err)
	default:
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
	machine.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
	trailing, trailingErr := reader.ReadByte()
	machine.SetReadDeadline(time.Time{})
	if trailingErr == nil && trailing != '?' {
		t.Fatalf("unexpected trailing wire byte after final Idle: %q", trailing)
	}
	if err := <-done; err != nil {
		t.Fatalf("stream after final-phase resume: %v", err)
	}
}

func TestStreamControlledPauseDuringCleanupAckSurvivesAckOrdering(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	done := make(chan error, 1)
	states := make(chan string, 16)
	go func() {
		done <- StreamControlled(ctx, client, "G1 X1", commands, nil, func(state string) { states <- state })
	}()
	reader := bufio.NewReader(machine)
	readByte := func(want byte) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire byte = %q, want %q (%v)", got, want, err)
		}
	}
	readLine := func(want string) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadString('\n')
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire line = %q, want %q (%v)", got, want, err)
		}
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	readLine("G1 X1\n")
	io.WriteString(machine, "ok\r\n")
	readLine("M5\n")
	commands <- Pause
	readByte('!')
	readByte('?')
	// Cleanup's ack can race the fresh status response to the same controller
	// read pump. It must not replace the still-required pause query role.
	io.WriteString(machine, "ok\r\n<Hold:0|MPos:1,0,0>\r\n")
	for {
		readByte('?')
		io.WriteString(machine, "<Hold:0|MPos:1,0,0>\r\n")
		select {
		case state := <-states:
			if state == "Hold:0" {
				goto confirmedHold
			}
		case <-ctx.Done():
			t.Fatalf("pause was not confirmed after cleanup ack: %v", ctx.Err())
		}
	}

confirmedHold:
	commands <- Resume
	resumeDeadline := time.Now().Add(time.Second)
	for {
		machine.SetReadDeadline(resumeDeadline)
		got, err := reader.ReadByte()
		if err != nil {
			t.Fatalf("wire after cleanup ack and Resume: want ~; got no byte: %v", err)
		}
		if got == '~' {
			machine.SetReadDeadline(time.Time{})
			break
		}
		if got != '?' {
			t.Fatalf("wire after cleanup ack and Resume: unexpected %q", got)
		}
		io.WriteString(machine, "<Hold:0|MPos:1,0,0>\r\n")
	}
	machine.SetReadDeadline(time.Time{})
	for {
		readByte('?')
		io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
		select {
		case state := <-states:
			if state == "Run" {
				goto resumed
			}
		case <-ctx.Done():
			t.Fatalf("resume was not confirmed: %v", ctx.Err())
		}
	}

resumed:
	deadline := time.Now().Add(time.Second)
	for {
		machine.SetReadDeadline(deadline)
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil {
			select {
			case streamErr := <-done:
				if streamErr != nil {
					t.Fatalf("stream after final Idle: %v", streamErr)
				}
				return
			case <-ctx.Done():
				t.Fatalf("stream did not complete after resumed Idle: %v (context %v)", err, ctx.Err())
			}
		}
		if got != '?' {
			t.Fatalf("unexpected wire byte after resume: %q", got)
		}
		io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
		select {
		case streamErr := <-done:
			if streamErr != nil {
				t.Fatalf("stream after final Idle: %v", streamErr)
			}
			return
		default:
		}
	}
}

func TestStreamControlledNewPauseCancelsUnconfirmedResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	done := make(chan error, 1)
	states := make(chan string, 16)
	applied := make(chan Command, 8)
	go func() {
		done <- StreamControlled(ctx, client, "G1 X1\nG1 X2", commands, nil, func(state string) { states <- state }, func(cmd Command) { applied <- cmd })
	}()
	reader := bufio.NewReader(machine)
	readByte := func() byte {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil {
			t.Fatalf("read controller command: %v", err)
		}
		return got
	}
	readLine := func(want string) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadString('\n')
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire line = %q, want %q (%v)", got, want, err)
		}
	}
	waitState := func(want string) {
		t.Helper()
		deadline := time.After(time.Second)
		for {
			select {
			case got := <-states:
				if got == want {
					return
				}
			case <-deadline:
				t.Fatalf("controller did not report %s", want)
			}
		}
	}
	waitApplied := func(want Command) {
		t.Helper()
		select {
		case got := <-applied:
			if got != want {
				t.Fatalf("applied control = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("control %q was not applied", want)
		}
	}
	respondUntilState := func(report, want string) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			select {
			case state := <-states:
				if state == want {
					return
				}
			default:
			}
			machine.SetReadDeadline(deadline)
			got, err := reader.ReadByte()
			machine.SetReadDeadline(time.Time{})
			if err != nil {
				t.Fatalf("waiting for fresh %s report: %v", want, err)
			}
			if got != '?' {
				t.Fatalf("wire byte %q while waiting for fresh %s report", got, want)
			}
			io.WriteString(machine, "<"+report+"|MPos:0,0,0>\r\n")
		}
	}
	waitCommand := func(want byte, report string) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			machine.SetReadDeadline(deadline)
			got, err := reader.ReadByte()
			machine.SetReadDeadline(time.Time{})
			if err != nil {
				t.Fatalf("waiting for wire byte %q: %v", want, err)
			}
			if got == want {
				return
			}
			if got != '?' {
				t.Fatalf("wire byte = %q while waiting for %q", got, want)
			}
			io.WriteString(machine, "<"+report+"|MPos:0,0,0>\r\n")
		}
	}
	readByte() // initial ?
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	waitState("Idle")
	readLine("G1 X1\n")
	commands <- Pause
	waitCommand('!', "Run")
	waitApplied(Pause)
	waitCommand('?', "Hold:0")
	io.WriteString(machine, "ok\r\n<Hold:0|MPos:0,0,0>\r\n")
	waitState("Hold:0")

	commands <- Resume
	waitCommand('~', "Hold:0")
	waitApplied(Resume)
	waitCommand('?', "Hold:0")
	io.WriteString(machine, "<Hold:0|MPos:0,0,0>\r\n")
	waitState("Hold:0") // still decelerated/held; Resume has not been confirmed

	for len(states) > 0 {
		<-states
	}
	commands <- Pause
	waitCommand('!', "Hold:0")
	waitApplied(Pause)
	respondUntilState("Hold:0", "Hold:0")
	waitCommand('?', "Idle")
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	deadline := time.Now().Add(900 * time.Millisecond)
	for {
		select {
		case err := <-done:
			t.Fatalf("stream finished on repeated Idle without a new Resume: %v", err)
		default:
		}
		machine.SetReadDeadline(deadline)
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			t.Fatalf("read while checking new pause gate: %v", err)
		}
		if got != '?' {
			t.Fatalf("wire byte %q; source line escaped the new pause gate", got)
		}
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	}

	for len(states) > 0 {
		<-states
	}
	commands <- Resume
	waitCommand('~', "Hold:0")
	waitApplied(Resume)
	for {
		got := readByte()
		if got == '?' {
			io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
			continue
		}
		if got != 'G' {
			t.Fatalf("wire byte %q after explicit Resume; want source line", got)
		}
		rest, err := reader.ReadString('\n')
		if err != nil || "G"+rest != "G1 X2\n" {
			t.Fatalf("resumed source = %q, %v", "G"+rest, err)
		}
		break
	}

	io.WriteString(machine, "ok\r\n")
	readLine("M5\n")
	io.WriteString(machine, "ok\r\n")
	for {
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil {
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("stream after final Idle: %v", err)
				}
				return
			case <-ctx.Done():
				t.Fatalf("stream did not finish after final Idle: %v", ctx.Err())
			}
		}
		if got != '?' {
			t.Fatalf("unexpected wire byte after cleanup: %q", got)
		}
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("stream after explicit resume: %v", err)
			}
			return
		case <-ctx.Done():
			t.Fatalf("stream did not finish after explicit resume: %v", ctx.Err())
		default:
		}
	}
}

func TestStreamControlledHealthyHoldSuspendsPendingAckDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 38*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan Command, 1)
	done := make(chan error, 1)
	states := make(chan string, 1)
	go func() {
		done <- StreamControlled(ctx, client, "G1 X1", commands, nil, func(state string) {
			select {
			case states <- state:
			default:
			}
		})
	}()

	wireBytes := make(chan byte, 64)
	peerErrors := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(machine)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				peerErrors <- err
				return
			}
			wireBytes <- b
		}
	}()

	resumeSeen := make(chan struct{}, 1)
	sourceSeen := make(chan struct{}, 1)
	cleanupSeen := make(chan struct{}, 1)
	releaseAck := make(chan struct{}, 1)
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		state, held := "Idle", false
		var line strings.Builder
		for {
			select {
			case b := <-wireBytes:
				switch b {
				case '?':
					if _, err := fmt.Fprintf(machine, "<%s|MPos:1,0,0>\r\n", state); err != nil {
						peerErrors <- err
						return
					}
				case '!':
					held, state = true, "Hold:0"
				case '~':
					held, state = false, "Run"
					resumeSeen <- struct{}{}
				case '\n':
					command := line.String()
					line.Reset()
					if command == "G1 X1" {
						if held {
							peerErrors <- fmt.Errorf("source command sent while held")
							return
						}
						sourceSeen <- struct{}{}
					} else if command == "M5" {
						if held {
							peerErrors <- fmt.Errorf("cleanup sent while held")
							return
						}
						state = "Idle"
						cleanupSeen <- struct{}{}
						if _, err := io.WriteString(machine, "ok\r\n"); err != nil {
							peerErrors <- err
							return
						}
					} else {
						peerErrors <- fmt.Errorf("unexpected controller line %q", command)
						return
					}
				default:
					line.WriteByte(b)
				}
			case <-releaseAck:
				if _, err := io.WriteString(machine, "ok\r\n"); err != nil {
					peerErrors <- err
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	awaitState := func(want string) {
		t.Helper()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		for {
			select {
			case got := <-states:
				if got == want {
					return
				}
			case err := <-peerErrors:
				t.Fatalf("wire peer: %v", err)
			case <-timer.C:
				t.Fatalf("controller did not report %s", want)
			}
		}
	}
	awaitState("Idle")
	select {
	case <-sourceSeen:
	case err := <-peerErrors:
		t.Fatalf("wire peer before source: %v", err)
	case <-time.After(time.Second):
		t.Fatal("source line was not sent")
	}
	commands <- Pause
	awaitState("Hold:0")

	// A healthy confirmed hold may wait indefinitely for a controller to ack
	// the line that was in flight when feed hold arrived.
	select {
	case err := <-done:
		t.Fatalf("stream returned with a pending ack during healthy Hold:0: %v", err)
	case err := <-peerErrors:
		t.Fatalf("wire peer during hold: %v", err)
	case <-time.After(31 * time.Second):
	}
	select {
	case err := <-done:
		t.Fatalf("pending ack deadline expired during healthy Hold:0: %v", err)
	default:
	}

	commands <- Resume
	select {
	case <-resumeSeen:
	case err := <-peerErrors:
		t.Fatalf("wire peer during resume: %v", err)
	case <-time.After(time.Second):
		select {
		case err := <-done:
			t.Fatalf("driver returned before resume byte: %v", err)
		case err := <-peerErrors:
			t.Fatalf("wire peer before resume byte: %v", err)
		default:
			t.Fatal("driver did not send resume byte; stream and peer remain open")
		}
	}
	awaitState("Run")
	releaseAck <- struct{}{}
	select {
	case <-cleanupSeen:
	case err := <-peerErrors:
		t.Fatalf("wire peer during cleanup: %v", err)
	case <-time.After(time.Second):
		t.Fatal("cleanup M5 was not sent after resumed ack")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream after resumed ack: %v", err)
		}
	case err := <-peerErrors:
		if err != io.EOF && err != net.ErrClosed {
			t.Fatalf("wire peer before completion: %v", err)
		}
		select {
		case streamErr := <-done:
			if streamErr != nil {
				t.Fatalf("stream after peer close: %v", streamErr)
			}
		case <-ctx.Done():
			t.Fatalf("stream did not complete after peer close: %v", ctx.Err())
		}
	case <-ctx.Done():
		t.Fatalf("stream did not complete after resume: %v", ctx.Err())
	}
	cancel()
	<-peerDone
}

func TestStreamCancellationJoinsReaderWhenStatusQueueIsFull(t *testing.T) {
	client, machine := net.Pipe()
	defer machine.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		reader := bufio.NewReader(machine)
		var first byte
		first, _ = reader.ReadByte()
		if first != '?' {
			return
		}
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		_, _ = reader.ReadString('\n')
		io.WriteString(machine, "ok\r\n")
		_, _ = io.WriteString(machine, strings.Repeat("<Run|MPos:0,0,0>\r\n", 1024))
	}()
	blocked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- StreamControlled(ctx, client, "G1 X1", nil, nil, func(state string) {
			if state == "Run" {
				select {
				case <-ctx.Done():
				case <-time.After(time.Second):
				}
				select {
				case <-blocked:
				default:
					close(blocked)
				}
			}
		})
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not join its reader after cancellation")
	}
	select {
	case <-peerDone:
	case <-time.After(time.Second):
		t.Fatal("controller flood writer remained blocked")
	}
}

func TestStreamStopsOnUnknownControllerState(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	done := make(chan error, 1)
	go func() { done <- Stream(context.Background(), client, "G1 X1\nG1 X2", nil) }()
	reader := bufio.NewReader(machine)
	readByte := func(want byte) {
		t.Helper()
		machine.SetReadDeadline(time.Now().Add(time.Second))
		got, err := reader.ReadByte()
		machine.SetReadDeadline(time.Time{})
		if err != nil || got != want {
			t.Fatalf("wire byte = %q, %v; want %q", got, err, want)
		}
	}
	readByte('?')
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	line, err := reader.ReadString('\n')
	if err != nil || line != "G1 X1\n" {
		t.Fatalf("source = %q, %v", line, err)
	}
	io.WriteString(machine, "ok\r\n")
	line, err = reader.ReadString('\n')
	if err != nil || line != "G1 X2\n" {
		t.Fatalf("second source = %q, %v", line, err)
	}
	io.WriteString(machine, "<Door:0|MPos:0,0,0>\r\n")
	stopBytes := make(chan string, 1)
	go func() {
		b1, e1 := reader.ReadByte()
		b2, e2 := reader.ReadByte()
		if e1 != nil || e2 != nil {
			stopBytes <- ""
			return
		}
		stopBytes <- string([]byte{b1, b2})
	}()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "Door:0") {
		t.Fatalf("unsupported-state error = %v", err)
	}
	if got := <-stopBytes; got != "!\x18" {
		t.Fatalf("failure stop bytes = %q", got)
	}
}

func TestStreamValidatesWholeProgramBeforeSending(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	done := make(chan struct{})
	go func() {
		var b [1]byte
		_, err := machine.Read(b[:])
		if err == nil {
			t.Errorf("sent byte %q for invalid program", b[0])
		}
		close(done)
	}()
	err := Stream(context.Background(), client, "G0 X1\nM5!", nil)
	if err == nil {
		t.Fatal("expected invalid program error")
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection was written")
	}
}

func TestStreamReturnsControllerError(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	go func() {
		var b [32]byte
		machine.Read(b[:])
		io.WriteString(machine, "<Idle|MPos:0,0,0>\n")
		machine.Read(b[:])
		io.WriteString(machine, "error:2\n")
	}()
	err := Stream(context.Background(), client, "G1 Xbad", nil)
	if err == nil || !strings.Contains(err.Error(), "error:2") {
		t.Fatalf("error = %v", err)
	}
}

func TestStreamCancellationInterruptsBlockedResponse(t *testing.T) {
	client, machine := net.Pipe()
	defer machine.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Stream(ctx, client, "G0 X1", nil) }()
	var command [16]byte
	if _, err := machine.Read(command[:]); err != nil {
		t.Fatal(err)
	}
	if string(command[:1]) != "?" {
		t.Fatalf("initial query = %q", command[:1])
	}
	if _, err := io.WriteString(machine, "<Idle|MPos:0,0,0>\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := machine.Read(command[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(time.Second):
		t.Fatal("Stream did not stop after cancellation")
	}
}

func TestSimulatorStreamsThroughPublicWireBoundary(t *testing.T) {
	conn := NewSimulator()
	defer conn.Close()
	var acked, total int
	if err := Stream(context.Background(), conn, "G1 X1\nM5", func(a, n int) { acked, total = a, n }); err != nil {
		t.Fatal(err)
	}
	if acked != 2 || total != 2 {
		t.Fatalf("progress = %d/%d", acked, total)
	}
}

func TestStreamReturnsImmediatelyOnAlarm(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	go func() { var b [32]byte; machine.Read(b[:]); io.WriteString(machine, "ALARM:1\r\n") }()
	started := time.Now()
	err := Stream(context.Background(), client, "G0 X1", nil)
	if err == nil || !strings.Contains(err.Error(), "ALARM:1") {
		t.Fatalf("error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("alarm response was not handled promptly")
	}
}

func TestValidateReturnsExecutableLineCount(t *testing.T) {
	n, err := Validate("\nG0 X1\r\n\nM5\n")
	if err != nil || n != 2 {
		t.Fatalf("Validate = %d, %v", n, err)
	}
	if _, err := Validate(" \r\n\t"); err == nil {
		t.Fatal("expected empty program rejection")
	}
}

func TestSimulatorImplementsRealtimeHoldResumeAndReset(t *testing.T) {
	conn := NewSimulator()
	defer conn.Close()
	reader := bufio.NewReader(conn)
	write := func(s string) {
		if _, err := io.WriteString(conn, s); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(line)
	}
	write("G1 X1\n")
	if got := read(); got != "ok" {
		t.Fatalf("ack = %q", got)
	}
	write("?")
	if got := read(); !strings.HasPrefix(got, "<Run|") {
		t.Fatalf("motion state = %q", got)
	}
	write("!")
	write("?")
	if got := read(); !strings.HasPrefix(got, "<Hold:1|") && !strings.HasPrefix(got, "<Hold:0|") {
		t.Fatalf("hold state = %q", got)
	}
	write("~")
	write("?")
	if got := read(); !strings.HasPrefix(got, "<Run|") {
		t.Fatalf("resume state = %q", got)
	}
	write("\x18")
	write("?")
	if got := read(); !strings.HasPrefix(got, "<Idle|") {
		t.Fatalf("reset state = %q", got)
	}
}

func TestStreamRejectsBusyControllerBeforeSendingProgram(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	go func() { var b [16]byte; machine.Read(b[:]); io.WriteString(machine, "<Run|MPos:0,0,0>\r\n") }()
	err := Stream(context.Background(), client, "G0 X1", nil)
	if err == nil || !strings.Contains(err.Error(), "is Run") {
		t.Fatalf("error = %v", err)
	}
	machine.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	var b [16]byte
	if n, e := machine.Read(b[:]); e == nil {
		t.Fatalf("program bytes sent while busy: %q", b[:n])
	}
}

func TestStreamStripsCommentsWithoutSendingRealtimeBytes(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	go func() {
		var initial [1]byte
		machine.Read(initial[:])
		if string(initial[:]) != "?" {
			t.Errorf("initial query = %q", initial[:])
		}
		io.WriteString(machine, "<Idle|MPos:0,0,0>\n")
		reader := bufio.NewReader(machine)
		line, _ := reader.ReadString('\n')
		if line != "M5\n" {
			t.Errorf("semicolon comment output = %q", line)
		}
		io.WriteString(machine, "ok\n")
		line, _ = reader.ReadString('\n')
		if line != "G0 X1\n" {
			t.Errorf("parenthetical comment output = %q", line)
		}
		io.WriteString(machine, "ok\n")
		line, _ = reader.ReadString('\n')
		if line != "M5\n" {
			t.Errorf("beam-off cleanup = %q", line)
		}
		io.WriteString(machine, "ok\n")
		var status [1]byte
		reader.Read(status[:])
		io.WriteString(machine, "<Idle|MPos:0,0,0>\n")
	}()
	if err := Stream(context.Background(), client, "M5 ; ? comment\nG0 X1 (note ! laser)", nil); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsMalformedCommentsAndRealtimeProgramBytes(t *testing.T) {
	for _, program := range []string{"G0 X1 (nested (bad))", "G0 X1 (unclosed", "G0 X1 )", "G0 X1 ?"} {
		if _, err := Validate(program); err == nil {
			t.Errorf("Validate(%q) succeeded", program)
		}
	}
}

func TestStreamStopsMachineAfterControllerRejectsLaterLine(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	peerDone := make(chan error, 1)
	go func() {
		var b [64]byte
		if _, err := machine.Read(b[:]); err != nil {
			peerDone <- err
			return
		}
		if _, err := io.WriteString(machine, "<Idle|MPos:0,0,0>\n"); err != nil {
			peerDone <- err
			return
		}
		if _, err := machine.Read(b[:]); err != nil {
			peerDone <- err
			return
		}
		if _, err := io.WriteString(machine, "ok\n"); err != nil {
			peerDone <- err
			return
		}
		if _, err := machine.Read(b[:]); err != nil {
			peerDone <- err
			return
		}
		if _, err := io.WriteString(machine, "error:2\n"); err != nil {
			peerDone <- err
			return
		}
		n, err := machine.Read(b[:])
		if err != nil {
			peerDone <- err
			return
		}
		if got := string(b[:n]); got != "!\x18" {
			peerDone <- fmt.Errorf("stop bytes = %q", got)
			return
		}
		machine.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, err = machine.Read(b[:])
		if err == nil {
			peerDone <- fmt.Errorf("unexpected extra program bytes %q", b[:n])
			return
		}
		peerDone <- nil
	}()
	started := time.Now()
	err := Stream(context.Background(), client, "G1 X1\nG1 X2\nG1 X3", nil)
	if err == nil || !strings.Contains(err.Error(), "error:2") {
		t.Fatalf("Stream error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("error stop path was not bounded")
	}
	if err := <-peerDone; err != nil {
		t.Fatal(err)
	}
}

func TestStreamSendsBeamOffCleanupBeforeFinalIdle(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	go func() {
		reader := bufio.NewReader(machine)
		var status [1]byte
		machine.Read(status[:])
		io.WriteString(machine, "<Idle|MPos:0,0,0>\n")
		line, _ := reader.ReadString('\n')
		if line != "G1 X1\n" {
			t.Errorf("source line = %q", line)
		}
		io.WriteString(machine, "ok\n")
		line, _ = reader.ReadString('\n')
		if line != "M5\n" {
			t.Errorf("cleanup line = %q", line)
		}
		io.WriteString(machine, "ok\n")
		machine.Read(status[:])
		io.WriteString(machine, "<Idle|MPos:1,0,0>\n")
	}()
	var acked, total int
	if err := Stream(context.Background(), client, "G1 X1", func(a, n int) { acked, total = a, n }); err != nil {
		t.Fatal(err)
	}
	if acked != 1 || total != 1 {
		t.Fatalf("source progress = %d/%d", acked, total)
	}
}

// slowMovePeer is a scripted GRBL wire peer for long-move acknowledgement
// tests. It answers the readiness query with Idle, then reports status(elapsed)
// for every later query, where elapsed is measured from receipt of the first
// source line. It withholds that line's ok until ackAfter (never when zero),
// acknowledges M5 immediately, and then reports Idle.
type slowMovePeer struct {
	conn     net.Conn
	status   func(elapsed time.Duration) string
	ackAfter time.Duration

	lineSeen chan time.Time
	errs     chan error
	wireMu   sync.Mutex
	wire     strings.Builder
}

func newSlowMovePeer(conn net.Conn, ackAfter time.Duration, status func(time.Duration) string) *slowMovePeer {
	p := &slowMovePeer{conn: conn, status: status, ackAfter: ackAfter, lineSeen: make(chan time.Time, 1), errs: make(chan error, 1)}
	go p.serve()
	return p
}

func (p *slowMovePeer) wireBytes() string {
	p.wireMu.Lock()
	defer p.wireMu.Unlock()
	return p.wire.String()
}

func (p *slowMovePeer) serve() {
	bytes := make(chan byte, 64)
	go func() {
		defer close(bytes)
		reader := bufio.NewReader(p.conn)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			p.wireMu.Lock()
			p.wire.WriteByte(b)
			p.wireMu.Unlock()
			bytes <- b
		}
	}()
	var lineStart time.Time
	var ack <-chan time.Time
	finished := false
	var line strings.Builder
	reply := func(s string) bool { _, err := io.WriteString(p.conn, s); return err == nil }
	for {
		select {
		case b, ok := <-bytes:
			if !ok {
				return
			}
			switch b {
			case '?':
				status := "<Idle|MPos:0.000,0.000,0.000|Bf:15,128>"
				if !lineStart.IsZero() && !finished {
					status = p.status(time.Since(lineStart))
				}
				if !reply(status + "\r\n") {
					return
				}
			case '\n':
				command := line.String()
				line.Reset()
				switch {
				case command == "M5":
					finished = true
					if !reply("ok\r\n") {
						return
					}
				case lineStart.IsZero():
					lineStart = time.Now()
					p.lineSeen <- lineStart
					if p.ackAfter > 0 {
						ack = time.After(p.ackAfter)
					}
				default:
					select {
					case p.errs <- fmt.Errorf("unexpected controller line %q", command):
					default:
					}
					return
				}
			default:
				line.WriteByte(b)
			}
		case <-ack:
			ack = nil
			if !reply("ok\r\n") {
				return
			}
		}
	}
}

func movingRunStatus(elapsed time.Duration) string {
	return fmt.Sprintf("<Run|MPos:%.3f,0.000,0.000|Bf:0,100>", elapsed.Seconds())
}

func TestDefaultTimeoutsMatchDocumentedBounds(t *testing.T) {
	got := DefaultTimeouts()
	if got.Response != 30*time.Second || got.Liveness != 30*time.Second {
		t.Fatalf("DefaultTimeouts() = %+v; want 30 s response and liveness bounds", got)
	}
}

// GRBL withholds ok while its planner is full, so one long, slow move can keep
// a line unacknowledged far beyond the per-line response bound. This scales the
// issue #50 scenario (a 120 s ok against a 30 s bound) by the injected bounds.
func TestStreamCompletesWhenDelayedAckIsCoveredByRunProgress(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	timeouts := Timeouts{Response: time.Second, Liveness: time.Second}
	ackDelay := 4 * timeouts.Liveness // 120 s against 30 s bounds, scaled.
	peer := newSlowMovePeer(machine, ackDelay, movingRunStatus)

	var acked int
	started := time.Now()
	err := StreamWithOptions(ctx, client, "G1 X300 F300", Options{
		Timeouts:   timeouts,
		OnProgress: func(a, _ int) { acked = a },
	})
	if err != nil {
		t.Fatalf("stream with delayed ack and continuous Run progress: %v (wire %q)", err, peer.wireBytes())
	}
	if elapsed := time.Since(started); elapsed < ackDelay {
		t.Fatalf("stream completed after %v, before the delayed ack at %v", elapsed, ackDelay)
	}
	if acked != 1 {
		t.Fatalf("acknowledged lines = %d; want 1", acked)
	}
	if wire := peer.wireBytes(); strings.ContainsAny(wire, "!\x18") {
		t.Fatalf("healthy slow move received stop bytes: %q", wire)
	}
}

func TestStreamCompletesWhenDelayedAckIsCoveredByPlannerProgress(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	timeouts := Timeouts{Response: time.Second, Liveness: time.Second}
	ackDelay := 4 * timeouts.Liveness
	reports := 0
	peer := newSlowMovePeer(machine, ackDelay, func(time.Duration) string {
		reports++
		// Position is unchanged at reporting resolution, but planner occupancy moves.
		return fmt.Sprintf("<Run|MPos:1.000,0.000,0.000|Bf:%d,100>", reports%2)
	})
	if err := StreamWithOptions(ctx, client, "G1 X300 F0.001", Options{Timeouts: timeouts}); err != nil {
		t.Fatalf("stream with delayed ack and changing planner occupancy: %v (wire %q)", err, peer.wireBytes())
	}
}

func TestStreamFailsClosedWhenSilentControllerWithholdsAck(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	timeouts := Timeouts{Response: time.Second, Liveness: time.Second}
	silentAfterLine := make(chan time.Time, 1)
	var wireMu sync.Mutex
	var wire strings.Builder
	go func() {
		reader := bufio.NewReader(machine)
		readyAnswered := false
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			wireMu.Lock()
			wire.WriteByte(b)
			wireMu.Unlock()
			if b == '?' && !readyAnswered {
				readyAnswered = true
				io.WriteString(machine, "<Idle|MPos:0.000,0.000,0.000>\r\n")
			}
			if b == '\n' {
				select {
				case silentAfterLine <- time.Now():
				default:
				}
			}
		}
	}()
	err := StreamWithOptions(ctx, client, "G1 X300 F300", Options{Timeouts: timeouts})
	failedAt := time.Now()
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("silent controller error = %v; want a timeout", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("silent controller was not bounded by the driver: %v", err)
	}
	lineSentAt := <-silentAfterLine
	if bound := timeouts.Response + 400*time.Millisecond; failedAt.Sub(lineSentAt) > bound {
		t.Fatalf("silent controller failed after %v; documented bound is the %v response timeout", failedAt.Sub(lineSentAt), timeouts.Response)
	}
	wireMu.Lock()
	got := wire.String()
	wireMu.Unlock()
	if !strings.HasSuffix(got, "!\x18") {
		t.Fatalf("silent-controller wire = %q; want feed hold and soft reset", got)
	}
}

func TestStreamFailsClosedWhenRunProgressStopsWithoutAck(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	timeouts := Timeouts{Response: time.Second, Liveness: time.Second}
	moving := 2 * timeouts.Liveness
	peer := newSlowMovePeer(machine, 0, func(elapsed time.Duration) string {
		if elapsed > moving {
			elapsed = moving // Run continues, but the position freezes.
		}
		return movingRunStatus(elapsed)
	})
	err := StreamWithOptions(ctx, client, "G1 X300 F300", Options{Timeouts: timeouts})
	failedAt := time.Now()
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("stalled Run error = %v; want a timeout", err)
	}
	lineStart := <-peer.lineSeen
	frozenAt := lineStart.Add(moving)
	if failedAt.Before(frozenAt) {
		t.Fatalf("stream failed %v before motion stopped", frozenAt.Sub(failedAt))
	}
	if late := failedAt.Sub(frozenAt); late > timeouts.Liveness+600*time.Millisecond {
		t.Fatalf("stalled Run failed %v after progress stopped; liveness bound is %v", late, timeouts.Liveness)
	}
	if wire := peer.wireBytes(); !strings.HasSuffix(wire, "!\x18") {
		t.Fatalf("stalled-Run wire = %q; want feed hold and soft reset", wire)
	}
}
