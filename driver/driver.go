// Package driver streams validated G-code to a GRBL 1.1-compatible wire
// connection. It reports controller acknowledgements, then waits for Idle.
package driver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	// MaxLineLength is GRBL 1.1's 80-byte line buffer, excluding its NUL byte.
	MaxLineLength   = 79
	responseTimeout = 30 * time.Second
)

// Stream sends each non-empty program line and waits for its ok response. It
// polls status at GRBL's supported low rate after all lines are acknowledged,
// and returns only after observing an Idle report. The caller retains ownership
// of conn; cancellation closes it to interrupt a blocked read.
func Stream(ctx context.Context, conn io.ReadWriteCloser, program string, onProgress func(acked, total int)) (resultErr error) {
	if conn == nil {
		return fmt.Errorf("driver: nil connection")
	}
	lines, err := parseProgram(program)
	if err != nil {
		return err
	}
	if ctx == nil {
		return fmt.Errorf("driver: nil context")
	}

	done := make(chan struct{})
	watcherDone := make(chan struct{})
	var stateMu sync.Mutex
	var programSent, stopped bool
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			stateMu.Lock()
			stopped = true
			sent := programSent
			stateMu.Unlock()
			if sent {
				// Feed hold and soft reset are bounded best effort. Closing the
				// connection interrupts blocked writes and reads even for generic
				// io.ReadWriteCloser implementations.
				if deadlineConn, ok := conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
					_ = deadlineConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
				}
				closeTimer := time.AfterFunc(100*time.Millisecond, func() { _ = conn.Close() })
				_, _ = io.WriteString(conn, "!\x18")
				closeTimer.Stop()
			}
			_ = conn.Close()
		})
	}
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			stop()
		case <-done:
		}
	}()
	defer func() {
		if resultErr != nil {
			stateMu.Lock()
			sent := programSent
			stateMu.Unlock()
			if sent {
				stop()
			}
		}
		close(done)
		<-watcherDone
	}()

	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	if err := requireIdle(ctx, conn, rw.Reader); err != nil {
		return err
	}
	for i, line := range lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		stateMu.Lock()
		if stopped {
			stateMu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("driver: connection stopped before line %d", i+1)
		}
		programSent = true // an attempted write may have reached the controller
		stateMu.Unlock()
		if _, err := rw.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("driver: send line %d: %w", i+1, err)
		}
		if err := rw.Flush(); err != nil {
			return fmt.Errorf("driver: send line %d: %w", i+1, err)
		}
		reply, err := readReply(ctx, conn, rw.Reader)
		if err != nil {
			return fmt.Errorf("driver: line %d response: %w", i+1, err)
		}
		if strings.HasPrefix(reply, "error:") || strings.HasPrefix(reply, "ALARM:") || strings.HasPrefix(reply, "Grbl ") {
			return fmt.Errorf("driver: line %d: controller %s", i+1, reply)
		}
		if reply != "ok" {
			return fmt.Errorf("driver: line %d: unexpected controller reply %q", i+1, reply)
		}
		if onProgress != nil {
			onProgress(i+1, len(lines))
		}
	}

	// Explicitly switch the spindle/laser off after the uploaded program. This
	// cleanup command is not part of source progress accounting.
	if err := ctx.Err(); err != nil {
		return err
	}
	stateMu.Lock()
	if stopped {
		stateMu.Unlock()
		return fmt.Errorf("driver: connection stopped before beam-off cleanup")
	}
	programSent = true
	stateMu.Unlock()
	if _, err := rw.WriteString("M5\n"); err != nil {
		return fmt.Errorf("driver: send beam-off cleanup: %w", err)
	}
	if err := rw.Flush(); err != nil {
		return fmt.Errorf("driver: send beam-off cleanup: %w", err)
	}
	cleanupReply, err := readReply(ctx, conn, rw.Reader)
	if err != nil {
		return fmt.Errorf("driver: beam-off cleanup response: %w", err)
	}
	if cleanupReply != "ok" {
		return fmt.Errorf("driver: beam-off cleanup: controller %s", cleanupReply)
	}

	// GRBL status reports may be ignored while a prior query is pending; poll no
	// faster than 5 Hz and tolerate unrelated push messages.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.WriteString(conn, "?"); err != nil {
			return fmt.Errorf("driver: query status: %w", err)
		}
		deadline := time.Now().Add(responseTimeout)
		for {
			line, err := readLineUntil(ctx, conn, rw.Reader, deadline)
			if err != nil {
				return fmt.Errorf("driver: wait for Idle: %w", err)
			}
			if strings.HasPrefix(line, "ALARM:") || strings.HasPrefix(line, "Grbl ") {
				return fmt.Errorf("driver: controller reset/alarm: %s", line)
			}
			if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
				state := strings.SplitN(line[1:], "|", 2)[0]
				if state == "Idle" {
					return nil
				}
				break
			}
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// requireIdle performs a fresh status query before any program bytes are sent.
// A serial port may emit its startup banner when opened, so only this first
// handshake tolerates that banner; a state other than Idle fails closed.
func requireIdle(ctx context.Context, conn io.ReadWriteCloser, r *bufio.Reader) error {
	if _, err := io.WriteString(conn, "?"); err != nil {
		return fmt.Errorf("driver: query initial status: %w", err)
	}
	deadline := time.Now().Add(responseTimeout)
	for {
		line, err := readLineUntil(ctx, conn, r, deadline)
		if err != nil {
			return fmt.Errorf("driver: initial status: %w", err)
		}
		if strings.HasPrefix(line, "ALARM:") {
			return fmt.Errorf("driver: controller alarm before job: %s", line)
		}
		if strings.HasPrefix(line, "Grbl ") {
			continue
		}
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			state := strings.SplitN(line[1:len(line)-1], "|", 2)[0]
			if state == "Idle" {
				return nil
			}
			return fmt.Errorf("driver: controller is %s before job; operator readiness required", state)
		}
	}
}

func parseProgram(program string) ([]string, error) {
	var cleaned strings.Builder
	inParen, inSemicolon := false, false
	for i := 0; i < len(program); i++ {
		b := program[i]
		if b > 0x7e || (b < 0x20 && b != '\n' && b != '\r' && b != '\t') || b == 0x7f {
			return nil, fmt.Errorf("driver: unsupported byte 0x%02x at offset %d", b, i)
		}
		if inSemicolon {
			if b == '\n' {
				inSemicolon = false
				cleaned.WriteByte('\n')
			}
			continue
		}
		if inParen {
			switch b {
			case '(':
				return nil, fmt.Errorf("driver: nested parenthetical comment at offset %d", i)
			case ')':
				inParen = false
			case '\n':
				cleaned.WriteByte('\n')
			}
			continue
		}
		switch b {
		case '(':
			inParen = true
			continue
		case ')':
			return nil, fmt.Errorf("driver: unmatched closing parenthesis at offset %d", i)
		case ';':
			inSemicolon = true
			continue
		case '!', '?', '~', 0x18:
			return nil, fmt.Errorf("driver: realtime byte %q at offset %d", b, i)
		}
		cleaned.WriteByte(b)
	}
	if inParen {
		return nil, fmt.Errorf("driver: unclosed parenthetical comment")
	}
	raw := strings.Split(strings.ReplaceAll(cleaned.String(), "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for i, line := range raw {
		line = strings.TrimSpace(line)
		if len(line) > MaxLineLength {
			return nil, fmt.Errorf("driver: line %d exceeds %d bytes", i+1, MaxLineLength)
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return nil, fmt.Errorf("driver: program has no executable lines")
	}
	return lines, nil
}

// Validate checks the complete program without sending any bytes and returns
// the number of executable lines.
func Validate(program string) (int, error) {
	lines, err := parseProgram(program)
	if err != nil {
		return 0, err
	}
	return len(lines), nil
}

func readReply(ctx context.Context, conn io.ReadWriteCloser, r *bufio.Reader) (string, error) {
	deadline := time.Now().Add(responseTimeout)
	for {
		line, err := readLineUntil(ctx, conn, r, deadline)
		if err != nil {
			return "", err
		}
		if line == "ok" || strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "ALARM:") || strings.HasPrefix(line, "Grbl ") {
			return line, nil
		}
	}
}

func readLineUntil(ctx context.Context, conn io.ReadWriteCloser, r *bufio.Reader, deadline time.Time) (string, error) {
	timer := time.AfterFunc(time.Until(deadline), func() { _ = conn.Close() })
	defer timer.Stop()
	// ReadSlice is bounded by the bufio.Reader's fixed buffer; a malformed
	// controller cannot force unbounded memory growth with an unterminated line.
	raw, err := r.ReadSlice('\n')
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err == bufio.ErrBufferFull {
			return "", fmt.Errorf("controller response line exceeds %d bytes", len(raw))
		}
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}
