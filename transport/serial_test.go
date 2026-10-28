package transport_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/transport"
)

type fakeOpener struct {
	conn io.ReadWriteCloser
	err  error
}

func (f fakeOpener) Open(string, int) (io.ReadWriteCloser, error) { return f.conn, f.err }

func TestOpenSerialWithRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  context.Context
		port string
		baud int
	}{
		{name: "nil context", port: "COM1", baud: 115200},
		{name: "empty port", ctx: context.Background(), baud: 115200},
		{name: "zero baud", ctx: context.Background(), port: "COM1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := transport.OpenSerialWith(tc.ctx, tc.port, tc.baud, fakeOpener{}); err == nil {
				t.Fatal("expected configuration error")
			}
		})
	}
}

func TestOpenSerialWithPropagatesOpenError(t *testing.T) {
	want := errors.New("port busy")
	if _, err := transport.OpenSerialWith(context.Background(), "COM1", 115200, fakeOpener{err: want}); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenSerialWithCancellationInterruptsBlockedRead(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := transport.OpenSerialWith(ctx, "fake", 115200, fakeOpener{conn: left})
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := conn.Read(make([]byte, 1)); readDone <- err }()
	cancel()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("blocked read returned without an error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt the serial read")
	}
}
