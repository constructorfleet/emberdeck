package driver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

const grblSettingsReply = "$0=10\r\n$20=1\r\n$30=1000.000\r\n$31=0.000\r\n$32=1\r\n$130=300.000\r\n$131=200.000\r\n$132=50.000\r\nok\r\n"

func value(v float64) *float64 { return &v }

func TestStreamReadsSettingsAfterIdleAndBeforeFirstProgramByte(t *testing.T) {
	client, machine := net.Pipe()
	defer machine.Close()
	var got Settings
	settingsSeen := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- StreamWithOptions(context.Background(), client, "G0 X1", Options{OnSettings: func(s Settings) error {
			got = s
			close(settingsSeen)
			return nil
		}})
	}()
	reader := bufio.NewReader(machine)
	if b, _ := reader.ReadByte(); b != '?' {
		t.Fatalf("readiness query = %q", b)
	}
	io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
	if line, _ := readNonQuery(reader, machine); line != "$$\n" {
		t.Fatalf("settings read = %q, want $$ after Idle", line)
	}
	select {
	case <-settingsSeen:
		t.Fatal("settings reported before the controller finished the $$ reply")
	default:
	}
	io.WriteString(machine, grblSettingsReply)
	line, _ := readNonQuery(reader, machine)
	if line != "G0 X1\n" {
		t.Fatalf("first program line = %q", line)
	}
	<-settingsSeen
	want := Settings{SoftLimits: value(1), MaxSpindle: value(1000), MinSpindle: value(0), LaserMode: value(1), MaxTravelX: value(300), MaxTravelY: value(200)}
	if diff := want.Mismatches(got); len(diff) != 0 || got.SoftLimits == nil || got.MaxTravelX == nil || got.MaxTravelY == nil {
		t.Fatalf("parsed settings = %+v (diff %v)", got, diff)
	}
	io.WriteString(machine, "ok\r\n")
	if line, _ := readNonQuery(reader, machine); line != "M5\n" {
		t.Fatalf("cleanup = %q", line)
	}
	io.WriteString(machine, "ok\r\n")
	go func() {
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			if b == '?' {
				io.WriteString(machine, "<Idle|MPos:1,0,0>\r\n")
			}
		}
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStreamRefusesMismatchedSettingsWithoutWritingProgramOrSettings(t *testing.T) {
	client, machine := net.Pipe()
	defer machine.Close()
	expected := Settings{MaxSpindle: value(1000), MinSpindle: value(0), LaserMode: value(1)}
	done := make(chan error, 1)
	go func() {
		done <- StreamWithOptions(context.Background(), client, "G0 X1", Options{OnSettings: func(s Settings) error {
			if diff := s.Mismatches(expected); len(diff) > 0 {
				return errors.New(strings.Join(diff, "; "))
			}
			return nil
		}})
	}()
	var written strings.Builder
	captured := make(chan struct{})
	go func() {
		defer close(captured)
		reader := bufio.NewReader(machine)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			written.WriteByte(b)
			switch {
			case b == '?':
				io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
			case strings.HasSuffix(written.String(), "$$\n"):
				io.WriteString(machine, "$20=0\r\n$30=255\r\n$31=0\r\n$32=0\r\n$130=300\r\n$131=200\r\nok\r\n")
			}
		}
	}()
	err := <-done
	if err == nil {
		t.Fatal("stream accepted mismatched controller settings")
	}
	for _, field := range []string{"$30", "controller 255", "profile 1000", "$32", "controller 0", "profile 1"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("mismatch error %q lacks %q", err, field)
		}
	}
	if strings.Contains(err.Error(), "$31") {
		t.Fatalf("matching $31 reported as a mismatch: %v", err)
	}
	<-captured
	sent := strings.ReplaceAll(written.String(), "?", "")
	if sent != "$$\n" {
		t.Fatalf("controller received %q; want only the read-only $$ query (no program, settings write, hold, or reset)", sent)
	}
}

func TestStreamFailsClosedWhenSettingsReadIsRejected(t *testing.T) {
	for name, reply := range map[string]string{
		"error":  "error:8\r\n",
		"reset":  "Grbl 1.1h ['$' for help]\r\n",
		"alarm":  "ALARM:2\r\n",
		"broken": "$30=fast\r\nok\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			client, machine := net.Pipe()
			defer machine.Close()
			called := false
			done := make(chan error, 1)
			go func() {
				done <- StreamWithOptions(context.Background(), client, "G0 X1", Options{OnSettings: func(Settings) error { called = true; return nil }})
			}()
			var written strings.Builder
			go func() {
				reader := bufio.NewReader(machine)
				for {
					b, err := reader.ReadByte()
					if err != nil {
						return
					}
					written.WriteByte(b)
					if b == '?' {
						io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
					} else if strings.HasSuffix(written.String(), "$$\n") {
						io.WriteString(machine, reply)
					}
				}
			}()
			if err := <-done; err == nil {
				t.Fatal("stream continued after a failed settings read")
			}
			if called {
				t.Fatal("settings callback ran for an incomplete read")
			}
			if strings.Contains(written.String(), "G0") {
				t.Fatalf("program bytes sent after failed settings read: %q", written.String())
			}
		})
	}
}

func TestStreamSettingsReadTimesOut(t *testing.T) {
	client, machine := net.Pipe()
	defer machine.Close()
	go func() {
		reader := bufio.NewReader(machine)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			if b == '?' {
				io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
			}
		}
	}()
	started := time.Now()
	err := StreamWithOptions(context.Background(), client, "G0 X1", Options{OnSettings: func(Settings) error { return nil }, Timeouts: Timeouts{Response: 300 * time.Millisecond}})
	if err == nil || !strings.Contains(err.Error(), "settings") {
		t.Fatalf("err = %v, want settings timeout", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("settings timeout took %s", time.Since(started))
	}
}

func TestReadSettingsProbesWithReadOnlyQuery(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	var written strings.Builder
	go func() {
		reader := bufio.NewReader(machine)
		for {
			b, err := reader.ReadByte()
			if err != nil {
				return
			}
			written.WriteByte(b)
			if strings.HasSuffix(written.String(), "$$\n") {
				io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n[MSG:note]\r\n"+grblSettingsReply)
			}
		}
	}()
	settings, err := ReadSettings(context.Background(), client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if settings.MaxSpindle == nil || *settings.MaxSpindle != 1000 || settings.LaserMode == nil || *settings.LaserMode != 1 || settings.SoftLimits == nil || *settings.SoftLimits != 1 {
		t.Fatalf("settings = %+v", settings)
	}
	if written.String() != "$$\n" {
		t.Fatalf("probe wrote %q", written.String())
	}
}

func TestReadSettingsRefusesResetBanner(t *testing.T) {
	client, machine := net.Pipe()
	defer client.Close()
	defer machine.Close()
	go func() {
		reader := bufio.NewReader(machine)
		reader.ReadString('\n')
		io.WriteString(machine, "\r\nGrbl 1.1h ['$' for help]\r\n")
	}()
	_, err := ReadSettings(context.Background(), client, time.Second)
	if err == nil || !strings.Contains(err.Error(), "reset") {
		t.Fatalf("err = %v, want reset refusal", err)
	}
}

func TestMismatchesReportsMissingExpectedValues(t *testing.T) {
	reported := Settings{MaxSpindle: value(1000)}
	diff := reported.Mismatches(Settings{MaxSpindle: value(1000), LaserMode: value(1)})
	if len(diff) != 1 || !strings.Contains(diff[0], "$32") || !strings.Contains(diff[0], "not reported") {
		t.Fatalf("diff = %v", diff)
	}
}

func TestSimulatorReportsDefaultLaserSettings(t *testing.T) {
	conn := NewSimulator()
	defer conn.Close()
	settings, err := ReadSettings(context.Background(), conn, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	want := Settings{MaxSpindle: value(1000), MinSpindle: value(0), LaserMode: value(1)}
	if diff := settings.Mismatches(want); len(diff) != 0 {
		t.Fatalf("simulator settings differ: %v", diff)
	}
}

// readNonQuery returns the next newline-terminated line, answering realtime
// status queries with Idle.
func readNonQuery(reader *bufio.Reader, machine io.Writer) (string, error) {
	var line strings.Builder
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return line.String(), err
		}
		if b == '?' {
			io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
			continue
		}
		line.WriteByte(b)
		if b == '\n' {
			return line.String(), nil
		}
	}
}
