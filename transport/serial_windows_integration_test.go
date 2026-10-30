//go:build windows

package transport_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/transport"
)

// This opt-in integration test uses a virtual COM pair created by the
// Windows COM feasibility workflow. It exercises EmberDeck's production
// Windows port enumeration and serial opener without claiming USB hardware.
func TestWindowsVirtualCOMPairRoundTrip(t *testing.T) {
	if os.Getenv("EMBERDECK_WINDOWS_COM_SMOKE") != "1" {
		t.Skip("set EMBERDECK_WINDOWS_COM_SMOKE=1 after installing the virtual COM pair")
	}
	ports, err := transport.ListSerialPorts()
	if err != nil {
		t.Fatalf("list serial ports: %v", err)
	}
	if !slices.Contains(ports, "COM10") || !slices.Contains(ports, "COM11") {
		t.Fatalf("virtual COM pair missing from production port list: %v", ports)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	left, err := transport.OpenSerial(ctx, "COM10", 115200)
	if err != nil {
		t.Fatalf("open COM10 through production serial transport: %v", err)
	}
	defer left.Close()
	right, err := transport.OpenSerial(ctx, "COM11", 115200)
	if err != nil {
		t.Fatalf("open COM11 through production serial transport: %v", err)
	}
	defer right.Close()

	want := []byte("emberdeck-windows-com-smoke")
	if n, err := left.Write(want); err != nil || n != len(want) {
		t.Fatalf("write through COM10: wrote %d of %d bytes: %v", n, len(want), err)
	}
	got := make([]byte, len(want))
	readDone := make(chan error, 1)
	go func() {
		_, readErr := io.ReadFull(right, got)
		readDone <- readErr
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatalf("read through COM11: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for COM11; received %q", got)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("COM pair payload mismatch: got %q, want %q", got, want)
	}
}
