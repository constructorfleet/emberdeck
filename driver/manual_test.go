package driver

import (
	"bufio"
	"context"
	"github.com/ConstructorFleet/emberdeck/toolpath"
	"io"
	"math"
	"net"
	"strings"
	"testing"
	"time"
)

func TestJogRequiresConfirmationAndIdleBeforeMotion(t *testing.T) {
	t.Run("confirmation", func(t *testing.T) {
		client, machine := net.Pipe()
		defer client.Close()
		defer machine.Close()
		if err := Jog(context.Background(), client, JogRequest{Axis: "X", DistanceMM: 1, FeedMMMin: 100, Confirmed: false}); err == nil {
			t.Fatal("Jog accepted a motion request without operator confirmation")
		}
		machine.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		var b [1]byte
		if _, err := machine.Read(b[:]); err == nil {
			t.Fatalf("unconfirmed jog wrote controller byte %q", b[:])
		}
	})

	t.Run("idle", func(t *testing.T) {
		client, machine := net.Pipe()
		defer client.Close()
		defer machine.Close()
		got := make(chan string, 1)
		go func() {
			reader := bufio.NewReader(machine)
			b := make([]byte, 1)
			_, _ = machine.Read(b)
			io.WriteString(machine, "<Run|MPos:0,0,0>\r\n")
			// Capture any unexpected command or motion byte sent after Run.
			_ = machine.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			line, _ := reader.ReadString('\n')
			got <- line
		}()
		err := Jog(context.Background(), client, JogRequest{Axis: "X", DistanceMM: 1, FeedMMMin: 100, Confirmed: true})
		if err == nil || !strings.Contains(err.Error(), "Idle") {
			t.Fatalf("Jog with Run status error = %v; want an idle-state refusal", err)
		}
		if wire := <-got; wire != "" {
			t.Fatalf("Jog emitted controller command %q while controller was Run", wire)
		}
	})
}

func TestJogSendsBoundedIncrementalCommandAndCancelsOnDeadline(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	wire := make(chan []byte, 5)
	go func() {
		reader := bufio.NewReader(machine)
		query, _ := reader.ReadByte()
		wire <- []byte{query}
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		laserOff, _ := reader.ReadString('\n')
		wire <- []byte(laserOff)
		io.WriteString(machine, "ok\r\n")
		query, _ = reader.ReadByte()
		wire <- []byte{query}
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		line, _ := reader.ReadString('\n')
		wire <- []byte(line)
		io.WriteString(machine, "ok\r\n")
		cancel, _ := reader.ReadByte()
		wire <- []byte{cancel}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := Jog(ctx, client, JogRequest{Axis: "X", DistanceMM: 10, FeedMMMin: 600, Confirmed: true})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Jog deadline error = %v; want bounded cancellation", err)
	}
	if got := string(<-wire); got != "?" {
		t.Fatalf("initial wire byte = %q; want status query", got)
	}
	if got := string(<-wire); got != "M5\n" {
		t.Fatalf("laser-off command = %q; want M5 before jogging", got)
	}
	if got := string(<-wire); got != "?" {
		t.Fatalf("pre-jog status query = %q; want a fresh idle check after M5", got)
	}
	if got := string(<-wire); got != "$J=G91 G21 X10 F600\n" {
		t.Fatalf("jog command = %q; want a bounded incremental GRBL jog", got)
	}
	if got := string(<-wire); got != "\x85" {
		t.Fatalf("jog cancel byte = %q; want 0x85", got)
	}
}

func TestJogWaitsForAcknowledgedIdleBeforeReturning(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	commands := make(chan string, 6)
	go func() {
		reader := bufio.NewReader(machine)
		readByte := func() { b, _ := reader.ReadByte(); commands <- string(b) }
		readLine := func() { line, _ := reader.ReadString('\n'); commands <- line }
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		io.WriteString(machine, "ok\r\n")
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		io.WriteString(machine, "ok\r\n")
		readByte()
		io.WriteString(machine, "<Run|MPos:1,0,0>\r\n")
		readByte()
		io.WriteString(machine, "<Idle|MPos:2,0,0>\r\n")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := Jog(ctx, client, JogRequest{Axis: "X", DistanceMM: 2, FeedMMMin: 300, Confirmed: true}); err != nil {
		t.Fatalf("Jog: %v", err)
	}
	want := []string{"?", "M5\n", "?", "$J=G91 G21 X2 F300\n", "?", "?"}
	for i, expected := range want {
		if got := <-commands; got != expected {
			t.Fatalf("wire command %d = %q; want %q", i+1, got, expected)
		}
	}
}

func TestJogRejectsUnboundedOrInvalidMotion(t *testing.T) {
	for _, request := range []JogRequest{
		{Axis: "X", DistanceMM: 26, FeedMMMin: 100, Confirmed: true},
		{Axis: "Q", DistanceMM: 1, FeedMMMin: 100, Confirmed: true},
		{Axis: "Y", DistanceMM: 1, FeedMMMin: 3001, Confirmed: true},
		{Axis: "Z", DistanceMM: 0, FeedMMMin: 100, Confirmed: true},
	} {
		if err := Jog(context.Background(), nil, request); err == nil {
			t.Fatalf("Jog accepted invalid motion %+v", request)
		}
	}
}

func TestManualCommandHomeWireRequiresLaserOffAndIdle(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	wire := make(chan string, 8)
	go func() {
		r := bufio.NewReader(machine)
		b, _ := r.ReadByte()
		wire <- string([]byte{b})
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		line, _ := r.ReadString('\n')
		wire <- line
		io.WriteString(machine, "ok\r\n")
		b, _ = r.ReadByte()
		wire <- string([]byte{b})
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		line, _ = r.ReadString('\n')
		wire <- line
		io.WriteString(machine, "ok\r\n")
		b, _ = r.ReadByte()
		wire <- string([]byte{b})
		io.WriteString(machine, "<Home|MPos:0,0,0>\r\n")
		b, _ = r.ReadByte()
		wire <- string([]byte{b})
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	}()
	if err := ManualCommand(context.Background(), client, ManualCommandRequest{Action: "home", Confirmed: true}); err != nil {
		t.Fatalf("ManualCommand home: %v", err)
	}
	want := []string{"?", "M5\n", "?", "$H\n", "?"}
	for i, w := range want {
		if got := <-wire; got != w {
			t.Fatalf("wire %d = %q; want %q", i+1, got, w)
		}
	}
}

func TestManualCommandFrameBoundsAreFiniteAndBounded(t *testing.T) {
	for _, bounds := range []toolpath.Bounds{
		{Min: toolpath.Point{X: 0, Y: 0}, Max: toolpath.Point{X: 501, Y: 10}},
		{Min: toolpath.Point{X: 2, Y: 0}, Max: toolpath.Point{X: 1, Y: 10}},
		{Min: toolpath.Point{X: 0, Y: 0}, Max: toolpath.Point{X: math.Inf(1), Y: 10}},
	} {
		if err := ManualCommand(context.Background(), nil, ManualCommandRequest{Action: "frame", Confirmed: true, Bounds: bounds}); err == nil {
			t.Fatalf("accepted invalid bounds %+v", bounds)
		}
	}
}

func TestManualCommandUnlockAndSetZeroWire(t *testing.T) {
	for _, tc := range []struct{ action, state, want string }{
		{"unlock", "Alarm:1", "$X\n"},
		{"set_zero", "Idle", "G10 L20 P1 X0 Y0\n"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			client, machine := net.Pipe()
			defer client.Close()
			defer machine.Close()
			wire := make(chan string, 2)
			go func() {
				r := bufio.NewReader(machine)
				b, _ := r.ReadByte()
				wire <- string([]byte{b})
				io.WriteString(machine, "<"+tc.state+"|MPos:0,0,0>\r\n")
				line, _ := r.ReadString('\n')
				wire <- line
				io.WriteString(machine, "ok\r\n")
			}()
			if err := ManualCommand(context.Background(), client, ManualCommandRequest{Action: tc.action, Confirmed: true}); err != nil {
				t.Fatalf("ManualCommand %s: %v", tc.action, err)
			}
			if got := <-wire; got != "?" {
				t.Fatalf("initial query = %q", got)
			}
			if got := <-wire; got != tc.want {
				t.Fatalf("wire command = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestManualCommandFrameWireIsBoundedAndLaserOff(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	wire := make(chan string, 12)
	go func() {
		r := bufio.NewReader(machine)
		b, _ := r.ReadByte()
		wire <- string([]byte{b})
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		line, _ := r.ReadString('\n')
		wire <- line
		io.WriteString(machine, "ok\r\n")
		b, _ = r.ReadByte()
		wire <- string([]byte{b})
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		for i := 0; i < 5; i++ {
			line, _ = r.ReadString('\n')
			wire <- line
			io.WriteString(machine, "ok\r\n")
		}
		b, _ = r.ReadByte()
		wire <- string([]byte{b})
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	}()
	bounds := toolpath.Bounds{Min: toolpath.Point{X: 1, Y: 2}, Max: toolpath.Point{X: 5, Y: 7}}
	if err := ManualCommand(context.Background(), client, ManualCommandRequest{Action: "frame", Confirmed: true, Bounds: bounds}); err != nil {
		t.Fatalf("ManualCommand frame: %v", err)
	}
	want := []string{"?", "M5\n", "?", "G90 G21\n", "G0 X1 Y2\n", "G0 X5 Y2\n", "G0 X5 Y7\n", "G0 X1 Y2\n", "?"}
	for i, w := range want {
		if got := <-wire; got != w {
			t.Fatalf("wire %d = %q; want %q", i+1, got, w)
		}
	}
}

func TestManualFrameDeadlineConfirmsFeedHoldBeforeReturning(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	wire := make(chan string, 16)
	holdQuery := make(chan struct{}, 1)
	allowHoldStop := make(chan struct{})
	holdReported := make(chan struct{})
	go func() {
		r := bufio.NewReader(machine)
		readQuery := func() { b, _ := r.ReadByte(); wire <- string([]byte{b}) }
		readLine := func() string { line, _ := r.ReadString('\n'); return line }
		readQuery()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		wire <- readLine()
		io.WriteString(machine, "ok\r\n")
		readQuery()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		for i := 0; i < 5; i++ {
			wire <- readLine()
			io.WriteString(machine, "ok\r\n")
		}
		holdRequested := false
		for {
			b, e := r.ReadByte()
			if e != nil {
				return
			}
			wire <- string([]byte{b})
			if b == '?' {
				if holdRequested {
					holdQuery <- struct{}{}
					<-allowHoldStop
					io.WriteString(machine, "<Hold:0|MPos:1,1,0>\r\n")
					close(holdReported)
				} else {
					io.WriteString(machine, "<Run|MPos:1,1,0>\r\n")
				}
			}
			if b == '!' {
				holdRequested = true
				io.WriteString(machine, "<Hold:1|MPos:1,1,0>\r\n")
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	bounds := toolpath.Bounds{Min: toolpath.Point{X: 0, Y: 0}, Max: toolpath.Point{X: 2, Y: 2}}
	done := make(chan error, 1)
	go func() {
		done <- ManualCommand(ctx, client, ManualCommandRequest{Action: "frame", Confirmed: true, Bounds: bounds})
	}()
	var sawFeedHold bool
	var sawConfirmedStop bool
	var observed []string
	timeout := time.After(time.Second)
	for !sawConfirmedStop {
		select {
		case got := <-wire:
			observed = append(observed, got)
			if got == "!" {
				sawFeedHold = true
			}
			if got == "?" && sawFeedHold {
				sawConfirmedStop = true
			}
		case <-timeout:
			t.Fatalf("bounded frame did not confirm feed hold before returning; observed %q", observed)
		}
	}
	if !sawFeedHold {
		t.Fatalf("bounded frame did not send GRBL feed hold; observed %q", observed)
	}
	<-holdQuery
	select {
	case err := <-done:
		t.Fatalf("bounded frame returned before GRBL confirmed the hold: %v", err)
	default:
	}
	close(allowHoldStop)
	<-holdReported
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "deadline") {
			t.Fatalf("frame deadline error = %v; want the original bounded-motion error after stop confirmation", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("bounded frame did not return after confirmed stop; observed %q", observed)
	}
}

func TestManualHomeDeadlineResetsAndReportsPositionLoss(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	wire := make(chan string, 16)
	go func() {
		r := bufio.NewReader(machine)
		readByte := func() byte {
			b, err := r.ReadByte()
			if err == nil {
				wire <- string([]byte{b})
			}
			return b
		}
		readLine := func() string {
			line, err := r.ReadString('\n')
			if err == nil {
				wire <- line
			}
			return line
		}
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		io.WriteString(machine, "ok\r\n")
		readByte()
		io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
		readLine()
		for {
			b := readByte()
			switch b {
			case '?':
				io.WriteString(machine, "<Alarm|MPos:0,0,0>\r\n")
			case 0x18:
				io.WriteString(machine, "ALARM:3\r\n")
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := ManualCommand(ctx, client, ManualCommandRequest{Action: "home", Confirmed: true})
	if err == nil || !strings.Contains(err.Error(), "deadline") || !strings.Contains(err.Error(), "position is lost") {
		t.Fatalf("missing homing acknowledgement error = %v; want bounded deadline and explicit position-loss semantics", err)
	}
	var sawReset bool
	for len(wire) > 0 {
		if <-wire == "\x18" {
			sawReset = true
		}
	}
	if !sawReset {
		t.Fatal("bounded homing did not send GRBL soft reset")
	}
}

func TestManualCommandInitialStatusWaitUsesRequestDeadline(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	query := make(chan byte, 1)
	go func() {
		b, err := bufio.NewReader(machine).ReadByte()
		if err == nil {
			query <- b
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := ManualCommand(ctx, client, ManualCommandRequest{Action: "set_zero", Confirmed: true})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("manual initial status error = %v; want request deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("manual initial status waited %s after request deadline", elapsed)
	}
	if got := <-query; got != '?' {
		t.Fatalf("initial status query = %q; want ?", got)
	}
}
