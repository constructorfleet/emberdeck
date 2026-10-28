package driver

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestParseStatusReportReturnsControllerPositionAndOffset(t *testing.T) {
	got, err := ParseStatusReport("<Run|MPos:10.25,-2,0.5|FS:400,0|WCO:2.25,-1,0>")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "Run" || got.MachinePosition == nil || *got.MachinePosition != (Vector{X: 10.25, Y: -2, Z: 0.5}) {
		t.Fatalf("parsed machine status = %#v", got)
	}
	if got.WorkOffset == nil || *got.WorkOffset != (Vector{X: 2.25, Y: -1}) {
		t.Fatalf("parsed work offset = %#v", got)
	}
}

func TestParseStatusReportReturnsExecutingLineMarker(t *testing.T) {
	got, err := ParseStatusReport("<Run|WPos:5,6,0|Ln:140>")
	if err != nil || got.LineNumber == nil || *got.LineNumber != 140 {
		t.Fatalf("parsed executing line marker = %#v, %v", got, err)
	}
	if _, err := ParseStatusReport("<Run|MPos:1,2,0|Ln:many>"); err == nil {
		t.Fatal("invalid line marker was accepted")
	}
}

func TestParseStatusReportAcceptsWorkPositionAndRejectsInvalidCoordinates(t *testing.T) {
	got, err := ParseStatusReport("<Idle|WPos:-1,2,0>")
	if err != nil || got.WorkPosition == nil || *got.WorkPosition != (Vector{X: -1, Y: 2}) {
		t.Fatalf("parsed work status = %#v, %v", got, err)
	}
	if _, err := ParseStatusReport("<Run|MPos:NaN,0,0>"); err == nil {
		t.Fatal("non-finite position was accepted")
	}
}

func TestStreamObservedRetainsIntermittentWorkOffset(t *testing.T) {
	client, controller := net.Pipe()
	reports := make(chan StatusReport, 8)
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		done <- StreamObserved(ctx, client, "G1 X12 Y15", nil, nil, nil, func(report StatusReport) { reports <- report })
	}()
	reader := bufio.NewReader(controller)
	controller.SetDeadline(time.Now().Add(2 * time.Second))
	readByte := func(want byte) {
		t.Helper()
		got, err := reader.ReadByte()
		if err != nil || got != want {
			t.Fatalf("wire byte = %q, %v; want %q", got, err, want)
		}
	}
	readLine := func(want string) {
		t.Helper()
		got, err := reader.ReadString('\n')
		if err != nil || got != want {
			t.Fatalf("wire line = %q, %v; want %q", got, err, want)
		}
	}
	readByte('?')
	_, _ = io.WriteString(controller, "<Idle|MPos:10,20,0|WCO:2,3,0>\r\n")
	first := <-reports
	if first.WorkPosition == nil || *first.WorkPosition != (Vector{X: 8, Y: 17}) {
		t.Fatalf("work position not derived from initial WCO: %#v", first)
	}
	readLine("G1 X12 Y15\n")
	_, _ = io.WriteString(controller, "ok\r\n")
	readLine("M5\n")
	_, _ = io.WriteString(controller, "ok\r\n")
	readByte('?')
	_, _ = io.WriteString(controller, "<Idle|MPos:14,18,0>\r\n")
	final := <-reports
	if final.WorkPosition == nil || *final.WorkPosition != (Vector{X: 12, Y: 15}) {
		t.Fatalf("intermittent WCO was not retained: %#v", final)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
