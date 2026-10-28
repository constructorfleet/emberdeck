package driver

import (
	"bufio"
	"context"
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
	go func() { var b [32]byte; machine.Read(b[:]); io.WriteString(machine, "error:2\n") }()
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
