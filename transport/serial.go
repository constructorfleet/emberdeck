// Package transport provides controller connection transports.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"go.bug.st/serial"
)

// Opener creates an io.ReadWriteCloser for a named serial port. It also allows
// applications to provide platform-specific openers and tests to use a fake.
type Opener interface {
	Open(portName string, baud int) (io.ReadWriteCloser, error)
}

type systemOpener struct{}

func (systemOpener) Open(portName string, baud int) (io.ReadWriteCloser, error) {
	return serial.Open(portName, &serial.Mode{BaudRate: baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
}

// OpenSerial opens a conventional 8-N-1 serial port at baud. Closing the
// context closes the port, interrupting an in-flight read or write.
func OpenSerial(ctx context.Context, portName string, baud int) (io.ReadWriteCloser, error) {
	return OpenSerialWith(ctx, portName, baud, systemOpener{})
}

// OpenSerialWith opens a serial connection through opener. The opener is a
// public system boundary so callers can adapt platform-specific ports and
// verify cancellation without physical hardware.
func OpenSerialWith(ctx context.Context, portName string, baud int, opener Opener) (io.ReadWriteCloser, error) {
	if ctx == nil {
		return nil, errors.New("serial transport: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(portName) == "" {
		return nil, errors.New("serial transport: port name is required")
	}
	if baud <= 0 {
		return nil, errors.New("serial transport: baud rate must be positive")
	}
	if opener == nil {
		return nil, errors.New("serial transport: opener is required")
	}
	conn, err := opener.Open(portName, baud)
	if err != nil {
		return nil, fmt.Errorf("serial transport: open %q: %w", portName, err)
	}
	if conn == nil {
		return nil, errors.New("serial transport: opener returned a nil connection")
	}
	wrapped := &contextConn{ReadWriteCloser: conn, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = wrapped.Close()
		case <-wrapped.done:
		}
	}()
	return wrapped, nil
}

// ListSerialPorts returns the serial port names reported by the current OS.
func ListSerialPorts() ([]string, error) { return serial.GetPortsList() }

type contextConn struct {
	io.ReadWriteCloser
	done chan struct{}
	once sync.Once
	err  error
}

func (c *contextConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.err = c.ReadWriteCloser.Close()
	})
	return c.err
}
