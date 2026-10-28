package driver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// ResetParser sends GRBL's realtime soft reset and confirms the restart banner
// plus a terminal Idle or Alarm status. It is intended for recovery when a
// G-code line may have been only partially transmitted; jog cancel and feed
// hold do not clear the parser's partial-line buffer.
func ResetParser(ctx context.Context, conn io.ReadWriteCloser, timeout time.Duration) (StatusReport, error) {
	if ctx == nil || conn == nil {
		return StatusReport{}, fmt.Errorf("driver: context and connection are required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	resetCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(resetCtx, func() { _ = conn.Close() })
	defer stop()
	write := func(raw string) error {
		n, err := io.WriteString(conn, raw)
		if err != nil {
			return err
		}
		if n != len(raw) {
			return io.ErrShortWrite
		}
		return nil
	}
	if err := write("\x18"); err != nil {
		return StatusReport{}, fmt.Errorf("driver: send controller soft reset: %w", err)
	}
	reader := bufio.NewReaderSize(conn, 256)
	deadline := time.Now().Add(timeout)
	banner := false
	for !banner {
		line, err := readLineUntil(resetCtx, conn, reader, deadline)
		if err != nil {
			return StatusReport{}, fmt.Errorf("driver: controller reset banner was not confirmed: %w", err)
		}
		banner = strings.HasPrefix(line, "Grbl ")
	}
	for {
		if err := write("?"); err != nil {
			return StatusReport{}, fmt.Errorf("driver: query controller after reset: %w", err)
		}
		line, err := readLineUntil(resetCtx, conn, reader, deadline)
		if err != nil {
			return StatusReport{}, fmt.Errorf("driver: terminal controller reset status was not confirmed: %w", err)
		}
		status, err := ParseStatusReport(line)
		if err != nil {
			continue
		}
		if status.State == "Idle" || status.State == "Alarm" {
			return status, nil
		}
	}
}

// ReadStatus sends one GRBL real-time status query and returns its parsed
// controller state. It leaves connection ownership with the caller.
func ReadStatus(ctx context.Context, conn io.ReadWriteCloser, timeout time.Duration) (StatusReport, error) {
	if ctx == nil || conn == nil {
		return StatusReport{}, fmt.Errorf("driver: context and connection are required")
	}
	if timeout <= 0 {
		timeout = responseTimeout
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := io.WriteString(conn, "?"); err != nil {
		return StatusReport{}, fmt.Errorf("driver: query controller status: %w", err)
	}
	reader := bufio.NewReader(conn)
	deadline := time.Now().Add(timeout)
	for {
		line, err := readLineUntil(ctx, conn, reader, deadline)
		if err != nil {
			return StatusReport{}, fmt.Errorf("driver: read controller status: %w", err)
		}
		status, err := ParseStatusReport(strings.TrimSpace(line))
		if err == nil {
			return status, nil
		}
	}
}
