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
	if got := read(); !strings.HasPrefix(got, "<Hold|") {
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
