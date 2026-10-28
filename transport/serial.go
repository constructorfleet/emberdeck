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

// ModemBits is the requested initial DTR/RTS output state for a serial port.
// The zero value disables both lines, avoiding an intentional assertion that
// can reset some GRBL controllers.
type ModemBits struct {
	DTR bool
	RTS bool
}

// ModemBitsOpener is an optional extension to Opener for backends that can
// configure DTR and RTS as part of opening a serial port. Existing Opener
// implementations remain compatible and are called through Open.
type ModemBitsOpener interface {
	OpenWithModemBits(portName string, baud int, bits ModemBits) (io.ReadWriteCloser, error)
}

type systemOpener struct{}

func (systemOpener) Open(portName string, baud int) (io.ReadWriteCloser, error) {
	return (systemOpener{}).OpenWithModemBits(portName, baud, ModemBits{})
}

func (systemOpener) OpenWithModemBits(portName string, baud int, bits ModemBits) (io.ReadWriteCloser, error) {
	return serial.Open(portName, &serial.Mode{
		BaudRate: baud, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit,
		InitialStatusBits: &serial.ModemOutputBits{DTR: bits.DTR, RTS: bits.RTS},
	})
}

// OpenSerial opens a conventional 8-N-1 serial port at baud. Closing the
// context closes the port, interrupting an in-flight read or write. It
// explicitly requests DTR and RTS off; use OpenSerialWithBits to choose another
// policy.
func OpenSerial(ctx context.Context, portName string, baud int) (io.ReadWriteCloser, error) {
	return OpenSerialWithBits(ctx, portName, baud, ModemBits{})
}

// OpenSerialWithBits opens a conventional 8-N-1 serial port with the requested
// initial modem output bits. Closing ctx closes the port.
func OpenSerialWithBits(ctx context.Context, portName string, baud int, bits ModemBits) (io.ReadWriteCloser, error) {
	return OpenSerialWithOpener(ctx, portName, baud, systemOpener{}, bits)
}

// OpenSerialWith opens a serial connection through opener. The opener is a
// public system boundary so callers can adapt platform-specific ports and
// verify cancellation without physical hardware. It requests DTR and RTS off;
// use OpenSerialWithOpener to supply another policy.
func OpenSerialWith(ctx context.Context, portName string, baud int, opener Opener) (io.ReadWriteCloser, error) {
	return OpenSerialWithOpener(ctx, portName, baud, opener, ModemBits{})
}

// OpenSerialWithOpener opens a connection through opener using the requested
// DTR/RTS policy. Legacy openers that implement only Opener continue to work;
// an opener can implement ModemBitsOpener to receive the policy.
func OpenSerialWithOpener(ctx context.Context, portName string, baud int, opener Opener, bits ModemBits) (io.ReadWriteCloser, error) {
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
	var conn io.ReadWriteCloser
	var err error
	if configurable, ok := opener.(ModemBitsOpener); ok {
		conn, err = configurable.OpenWithModemBits(portName, baud, bits)
	} else {
		conn, err = opener.Open(portName, baud)
	}
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
