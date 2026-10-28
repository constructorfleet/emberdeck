package driver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

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
