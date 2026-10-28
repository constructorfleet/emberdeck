package driver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
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
