// Package driver streams validated G-code to a GRBL 1.1-compatible wire
// connection. It reports controller acknowledgements, then waits for Idle.
package driver

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxLineLength is GRBL 1.1's 80-byte line buffer, excluding its NUL byte.
	MaxLineLength   = 79
	responseTimeout = 30 * time.Second
	livenessTimeout = 30 * time.Second
)

// Timeouts bounds how long a stream waits for controller evidence before it
// fails closed with a feed hold and soft reset. A zero field uses the matching
// DefaultTimeouts value.
type Timeouts struct {
	// Response bounds each controller reply: a line acknowledgement when no
	// motion progress is reported, a status-query response, missing status
	// reports while held or finishing, and a hold or resume transition.
	Response time.Duration
	// Liveness bounds the gap between fresh Run status reports that show
	// progress (a changed position or planner block count) while a line
	// acknowledgement is outstanding. Each such report extends that line's
	// acknowledgement deadline to at least this long after the report.
	Liveness time.Duration
}

// DefaultTimeouts returns the bounds used by Stream, StreamControlled, and
// StreamObserved: 30 s for responses and 30 s for motion-progress liveness.
func DefaultTimeouts() Timeouts {
	return Timeouts{Response: responseTimeout, Liveness: livenessTimeout}
}

// Options configures StreamWithOptions. Nil callbacks and a nil command channel
// are ignored.
type Options struct {
	// Commands carries Pause and Resume requests, as for StreamControlled.
	Commands <-chan Command
	// OnProgress receives acknowledged source line counts.
	OnProgress func(acked, total int)
	// OnState receives exact state fields from fresh status reports.
	OnState func(string)
	// OnStatus receives each fresh parsed status report, as for StreamObserved.
	OnStatus func(StatusReport)
	// OnApplied runs synchronously after a Pause or Resume byte is written.
	OnApplied func(Command)
	// Timeouts overrides the default controller bounds.
	Timeouts Timeouts
}

// Command controls a stream while it is running.
type Command string

const (
	Pause  Command = "pause"
	Resume Command = "resume"
)

// Stream sends each non-empty program line and waits for its ok response. It
// polls GRBL status and returns only after observing Idle. Stream closes conn
// when it returns, including on success.
func Stream(ctx context.Context, conn io.ReadWriteCloser, program string, onProgress func(acked, total int)) (resultErr error) {
	return StreamControlled(ctx, conn, program, nil, onProgress, nil)
}

// StreamControlled streams a program while routing Pause and Resume commands
// through the same GRBL connection. State callbacks receive exact state fields
// from observed status reports. Optional onApplied callbacks run synchronously
// after a Pause or Resume realtime byte is successfully written. It closes conn
// when it returns.
func StreamControlled(ctx context.Context, conn io.ReadWriteCloser, program string, commands <-chan Command, onProgress func(acked, total int), onState func(string), onApplied ...func(Command)) (resultErr error) {
	return StreamWithOptions(ctx, conn, program, Options{Commands: commands, OnProgress: onProgress, OnState: onState, OnApplied: firstApplied(onApplied)})
}

// StreamObserved behaves like StreamControlled and additionally reports each
// fresh parsed GRBL status response. Coordinates are in the controller's
// configured reporting units; callers must not compare them with millimetre
// paths unless those units have been confirmed.
func StreamObserved(ctx context.Context, conn io.ReadWriteCloser, program string, commands <-chan Command, onProgress func(acked, total int), onState func(string), onStatus func(StatusReport), onApplied ...func(Command)) (resultErr error) {
	return StreamWithOptions(ctx, conn, program, Options{Commands: commands, OnProgress: onProgress, OnState: onState, OnStatus: onStatus, OnApplied: firstApplied(onApplied)})
}

func firstApplied(callbacks []func(Command)) func(Command) {
	if len(callbacks) == 0 {
		return nil
	}
	return callbacks[0]
}

// StreamWithOptions streams a program with the behavior of StreamObserved and
// configurable controller timeouts. It closes conn when it returns.
func StreamWithOptions(ctx context.Context, conn io.ReadWriteCloser, program string, opts Options) (resultErr error) {
	commands, onProgress, onState, onStatus := opts.Commands, opts.OnProgress, opts.OnState, opts.OnStatus
	timeouts := DefaultTimeouts()
	if opts.Timeouts.Response < 0 || opts.Timeouts.Liveness < 0 {
		return fmt.Errorf("driver: negative timeout")
	}
	if opts.Timeouts.Response > 0 {
		timeouts.Response = opts.Timeouts.Response
	}
	if opts.Timeouts.Liveness > 0 {
		timeouts.Liveness = opts.Timeouts.Liveness
	}
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

	// The stream owns the connection so closing it always joins the bounded read
	// pump, including on successful completion.
	defer conn.Close()
	type controllerLine struct{ text string }
	readLines := make(chan controllerLine, 16)
	readErrors := make(chan error, 1)
	readerStop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		r := bufio.NewReader(conn)
		for {
			raw, err := r.ReadSlice('\n')
			if err != nil {
				readErrors <- err
				return
			}
			select {
			case readLines <- controllerLine{text: strings.TrimSpace(string(raw))}:
			case <-ctx.Done():
				return
			case <-readerStop:
				return
			}
		}
	}()
	defer func() { close(readerStop); _ = conn.Close(); <-readerDone }()
	write := func(s string) error {
		if deadlineConn, ok := conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadlineConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		}
		timer := time.AfterFunc(100*time.Millisecond, func() { _ = conn.Close() })
		_, err := io.WriteString(conn, s)
		timer.Stop()
		return err
	}
	controllerTouched := false
	cleanupStop := func() {
		if resultErr == nil || !controllerTouched {
			return
		}
		if deadlineConn, ok := conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadlineConn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		}
		closeTimer := time.AfterFunc(100*time.Millisecond, func() { _ = conn.Close() })
		_, _ = io.WriteString(conn, "!\x18")
		closeTimer.Stop()
	}
	defer cleanupStop()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	phase := "initial"
	index, sentIndex := 0, -1
	pending, cleanupPending := false, false
	gate, pauseSent, pauseConfirmed, resumeRequested, resuming := false, false, false, false, false
	externalHold := false
	lastStatus, ackDeadline := time.Now(), time.Time{}
	controlDeadline := time.Time{}
	queryOutstanding, querySentAt := false, time.Time{}
	queryRole, nextQueryRole := "", ""
	lastQueryWrite := time.Time{}
	var lastWorkOffset *Vector
	var progress motionProgress
	appliedCallback := opts.OnApplied
	sendQuery := func(role string) error {
		if queryOutstanding {
			nextQueryRole = role
			return nil
		}
		if wait := 200*time.Millisecond - time.Since(lastQueryWrite); wait > 0 {
			nextQueryRole = role
			return nil
		}
		now := time.Now()
		queryOutstanding, querySentAt, queryRole, lastQueryWrite = true, now, role, now
		if err := write("?"); err != nil {
			queryOutstanding = false
			return err
		}
		nextQueryRole = ""
		return nil
	}
	applyPause := func() error {
		gate, pauseSent, pauseConfirmed = true, true, false
		resumeRequested, resuming = false, false
		controlDeadline = time.Now().Add(timeouts.Response)
		controllerTouched = true
		if queryOutstanding {
			queryRole, nextQueryRole = "stale", "pause"
		}
		if err := write("!"); err != nil {
			return fmt.Errorf("driver: feed hold: %w", err)
		}
		if appliedCallback != nil {
			appliedCallback(Pause)
		}
		if err := sendQuery("pause"); err != nil {
			return fmt.Errorf("driver: query hold status: %w", err)
		}
		return nil
	}
	applyResume := func() error {
		if !pauseConfirmed {
			return nil
		}
		resumeRequested = true
		controlDeadline = time.Now().Add(timeouts.Response)
		if queryOutstanding {
			queryRole, nextQueryRole = "stale", "resume"
		}
		if err := write("~"); err != nil {
			return fmt.Errorf("driver: resume: %w", err)
		}
		resuming = true
		if appliedCallback != nil {
			appliedCallback(Resume)
		}
		if err := sendQuery("resume"); err != nil {
			return fmt.Errorf("driver: query resume status: %w", err)
		}
		return nil
	}
	if err := sendQuery("initial"); err != nil {
		return fmt.Errorf("driver: query initial status: %w", err)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Drain already queued controls before considering the next source line.
		// This makes a Pause delivered alongside an ack gate the very next line.
		for commands != nil {
			select {
			case cmd, ok := <-commands:
				if !ok {
					commands = nil
					continue
				}
				switch cmd {
				case Pause:
					if phase == "initial" {
						gate, pauseSent, pauseConfirmed, resumeRequested, resuming = true, false, false, false, false
						controlDeadline = time.Now().Add(timeouts.Response)
					} else {
						if err := applyPause(); err != nil {
							return err
						}
					}
				case Resume:
					resumeRequested = true
					if err := applyResume(); err != nil {
						return err
					}
				default:
					return fmt.Errorf("driver: unknown command %q", cmd)
				}
			default:
				goto controlsDrained
			}
		}
	controlsDrained:
		if gate && phase != "initial" && !pauseSent {
			if err := applyPause(); err != nil {
				return err
			}
		}
		if phase == "stream" && !pending && !gate && index < len(lines) {
			controllerTouched = true
			if err := write(lines[index] + "\n"); err != nil {
				return fmt.Errorf("driver: send line %d: %w", index+1, err)
			}
			pending, sentIndex = true, index
			controllerTouched = true
			ackDeadline = time.Now().Add(timeouts.Response)
		}
		if phase == "stream" && !pending && !gate && index == len(lines) {
			controllerTouched = true
			if err := write("M5\n"); err != nil {
				return fmt.Errorf("driver: send beam-off cleanup: %w", err)
			}
			pending, cleanupPending, phase = true, true, "cleanup"
			controllerTouched = true
			ackDeadline = time.Now().Add(timeouts.Response)
		}
		if pending && !gate && time.Now().After(ackDeadline) {
			return fmt.Errorf("driver: response timeout")
		}
		if (gate || phase == "initial" || phase == "final") && time.Since(lastStatus) > timeouts.Response {
			return fmt.Errorf("driver: status timeout")
		}
		if !controlDeadline.IsZero() && time.Now().After(controlDeadline) {
			return fmt.Errorf("driver: controller did not reach requested hold/resume state in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case cmd, ok := <-commands:
			if !ok {
				commands = nil
				continue
			}
			switch cmd {
			case Pause:
				if phase == "initial" {
					gate, pauseSent, pauseConfirmed = true, false, false
					controlDeadline = time.Now().Add(timeouts.Response)
					continue
				}
				if err := applyPause(); err != nil {
					return err
				}
			case Resume:
				resumeRequested = true
				if err := applyResume(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("driver: unknown command %q", cmd)
			}
		case <-ticker.C:
			if queryOutstanding && time.Since(querySentAt) >= timeouts.Response {
				return fmt.Errorf("driver: status response timeout")
			} else if !queryOutstanding {
				role := "normal"
				if nextQueryRole != "" {
					role, nextQueryRole = nextQueryRole, ""
				} else if phase == "initial" {
					role = "initial"
				} else if phase == "final" {
					role = "final"
				}
				if err := sendQuery(role); err != nil {
					return fmt.Errorf("driver: query status: %w", err)
				}
			}
		case event := <-readLines:
			line := event.text
			if strings.HasPrefix(line, "ALARM:") {
				return fmt.Errorf("driver: controller alarm: %s", line)
			}
			if strings.HasPrefix(line, "Grbl ") {
				phaseName := "during streaming"
				if phase == "initial" {
					phaseName = "during readiness"
				}
				return fmt.Errorf("driver: controller reset %s (%s); work coordinates must be re-verified before starting a job", phaseName, line)
			}
			if strings.HasPrefix(line, "error:") {
				return fmt.Errorf("driver: controller %s", line)
			}
			if line == "ok" {
				if !pending {
					continue
				}
				pending = false
				if cleanupPending {
					cleanupPending = false
					phase = "final"
					if queryOutstanding {
						queryRole, nextQueryRole = "stale", "final"
					} else {
						nextQueryRole = "final"
					}
				} else {
					index = sentIndex + 1
					if onProgress != nil {
						onProgress(index, len(lines))
					}
				}
				continue
			}
			if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
				report, reportErr := ParseStatusReport(line)
				state := ""
				if reportErr == nil {
					state = report.State
				} else {
					state = strings.SplitN(line[1:len(line)-1], "|", 2)[0]
				}
				responseRole := "unsolicited"
				if queryOutstanding {
					responseRole = queryRole
					queryOutstanding = false
					if nextQueryRole == "" {
						switch {
						case phase == "initial" && gate:
							nextQueryRole = "pause"
						case phase == "initial":
							// The readiness response completes the initial handshake.
						case pauseSent && !pauseConfirmed:
							nextQueryRole = "pause"
						case resuming:
							nextQueryRole = "resume"
						case phase == "final":
							nextQueryRole = "final"
						default:
							nextQueryRole = "normal"
						}
					}
					role := nextQueryRole
					nextQueryRole = ""
					if role != "" {
						if err := sendQuery(role); err != nil {
							return fmt.Errorf("driver: query status: %w", err)
						}
					}
				}
				fresh := responseRole != "stale" && responseRole != "unsolicited"
				if phase != "initial" && state != "Idle" && state != "Run" && state != "Hold" && state != "Hold:0" && state != "Hold:1" {
					return fmt.Errorf("driver: controller entered unsupported state %s", state)
				}
				if fresh {
					lastStatus = time.Now()
					// GRBL withholds ok while its planner is full, so a long move can
					// legitimately outlast the response bound. Fresh Run reports that
					// show motion or planner progress prove the controller is alive and
					// extend the pending acknowledgement by the liveness bound.
					if progress.observe(report, reportErr == nil, plannerBlocksAvailable(line)) && state == "Run" && pending && !gate {
						if extended := lastStatus.Add(timeouts.Liveness); extended.After(ackDeadline) {
							ackDeadline = extended
						}
					}
				}
				if onState != nil && (fresh || (phase == "initial" && responseRole == "initial")) {
					onState(state)
				}
				if fresh && onStatus != nil && reportErr == nil {
					if report.WorkOffset != nil {
						offset := *report.WorkOffset
						lastWorkOffset = &offset
					}
					if report.MachinePosition != nil && lastWorkOffset != nil {
						machine := *report.MachinePosition
						offset := *lastWorkOffset
						work := Vector{X: machine.X - offset.X, Y: machine.Y - offset.Y, Z: machine.Z - offset.Z, A: machine.A - offset.A}
						report.WorkPosition = &work
					}
					if report.WorkPosition != nil && lastWorkOffset != nil {
						work := *report.WorkPosition
						offset := *lastWorkOffset
						machine := Vector{X: work.X + offset.X, Y: work.Y + offset.Y, Z: work.Z + offset.Z, A: work.A + offset.A}
						report.MachinePosition = &machine
					}
					onStatus(report)
				}
				if phase == "initial" {
					if responseRole != "initial" {
						continue
					}
					if state != "Idle" {
						return fmt.Errorf("driver: controller is %s before job; operator readiness required", state)
					}
					phase = "stream"
					select {
					case cmd, ok := <-commands:
						if ok && cmd == Pause {
							gate = true
						}
						if !ok {
							commands = nil
						}
					default:
					}
					if gate {
						if err := applyPause(); err != nil {
							return err
						}
					}
					continue
				}
				if strings.HasPrefix(state, "Hold") {
					gate = true
					if !pauseSent {
						externalHold = true
						if err := applyPause(); err != nil {
							return err
						}
					}
					if state == "Hold:0" && fresh && responseRole == "pause" {
						pauseConfirmed = true
						controlDeadline = time.Time{}
					}
				} else if state == "Idle" {
					if pauseSent && fresh && responseRole == "pause" {
						pauseConfirmed = true
						controlDeadline = time.Time{}
					}
					if resuming && fresh && responseRole == "resume" {
						gate, pauseSent, pauseConfirmed, resumeRequested, resuming = false, false, false, false, false
						externalHold = false
						controlDeadline = time.Time{}
						if pending {
							ackDeadline = time.Now().Add(timeouts.Response)
						}
					}
					if phase == "final" && !gate && !resuming && fresh && responseRole == "final" {
						return nil
					}
				} else if state == "Run" {
					if resuming && fresh && responseRole == "resume" {
						gate, pauseSent, pauseConfirmed, resumeRequested, resuming = false, false, false, false, false
						externalHold = false
						controlDeadline = time.Time{}
						if pending {
							ackDeadline = time.Now().Add(timeouts.Response)
						}
					} else if fresh && gate && (pauseConfirmed || externalHold) {
						return fmt.Errorf("driver: controller resumed without explicit Resume command")
					}
				}
				if pauseConfirmed && resumeRequested && !resuming {
					if err := applyResume(); err != nil {
						return err
					}
				}
				continue
			}
		case err := <-readErrors:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("driver: read controller: %w", err)
		}
	}
}

// requireIdle performs a fresh status query before any program bytes are sent.
// A startup banner means the controller reset, so the job must be refused until
// the operator re-verifies work coordinates.
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
			return fmt.Errorf("driver: controller reset during readiness (%s); work coordinates must be re-verified before starting a job", line)
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
	parenStart := 0
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
			parenStart = i
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
		return nil, fmt.Errorf("driver: unclosed parenthetical comment at offset %d", parenStart)
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
		fields := strings.Fields(err.Error())
		for i, field := range fields {
			if field == "offset" && i+1 < len(fields) {
				offset, parseErr := strconv.Atoi(strings.TrimRight(fields[i+1], ":"))
				if parseErr == nil && offset >= 0 && offset <= len(program) {
					line := strings.Count(program[:offset], "\n") + 1
					return 0, fmt.Errorf("driver: line %d: %w", line, err)
				}
			}
		}
		if !strings.Contains(err.Error(), "driver: line ") {
			return 0, fmt.Errorf("driver: line 1: %w", err)
		}
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
