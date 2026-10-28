package driver

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ConstructorFleet/emberdeck/toolpath"
)

const (
	MaxJogDistanceMM = 25.0
	MaxJogFeedMMMin  = 3000.0
	maxJogDuration   = 10 * time.Second
	manualResponse   = 2 * time.Second
	manualPoll       = 200 * time.Millisecond
)

// JogRequest describes one incremental, metric GRBL jog. Confirmed must be
// set only after the operator confirms that the machine is clear to move.
type JogRequest struct {
	Axis       string
	DistanceMM float64
	FeedMMMin  float64
	Confirmed  bool
}

// Jog checks that the operator confirmed the motion and the controller is
// Idle, then sends one bounded incremental jog. The caller must hold exclusive
// manual-control ownership and must not share conn with a job stream; this
// function does not acquire a server lease. It owns and closes conn. A jog is
// canceled with GRBL's realtime 0x85 byte if its bounded context ends or the
// controller stops responding after the jog has been sent.
func Jog(ctx context.Context, conn io.ReadWriteCloser, request JogRequest) error {
	if ctx == nil {
		return errors.New("driver: context is required")
	}
	if err := ValidateJogRequest(request); err != nil {
		return err
	}
	if conn == nil {
		return errors.New("driver: controller connection is required")
	}
	bounded, cancel := context.WithTimeout(ctx, maxJogDuration)
	defer cancel()

	lines := make(chan string, 16)
	readErrors := make(chan error, 1)
	readerStop := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		reader := bufio.NewReaderSize(conn, 256)
		for {
			line, err := reader.ReadSlice('\n')
			if err != nil {
				select {
				case readErrors <- err:
				default:
				}
				return
			}
			select {
			case lines <- strings.TrimSpace(string(line)):
			case <-readerStop:
				return
			}
		}
	}()
	defer func() {
		close(readerStop)
		_ = conn.Close()
		<-readerDone
	}()

	write := func(raw string) error {
		if deadline, ok := conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = deadline.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		}
		closeTimer := time.AfterFunc(100*time.Millisecond, func() { _ = conn.Close() })
		n, err := io.WriteString(conn, raw)
		closeTimer.Stop()
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
		return err
	}
	resetAfterAmbiguousWrite := func(cause error) error {
		return resetManualParser(write, lines, readErrors, cause)
	}
	stopJog := func(cause error) error {
		if err := write("\x85"); err != nil {
			return fmt.Errorf("%w; driver: jog stop was not confirmed: could not send cancel: %v", cause, err)
		}
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(manualPoll)
		defer ticker.Stop()
		for {
			if err := write("?"); err != nil {
				return fmt.Errorf("%w; driver: jog stop was not confirmed: status query failed: %v", cause, err)
			}
			queryDeadline := time.NewTimer(manualPoll)
			awaitingStatus := true
			for awaitingStatus {
				select {
				case line := <-lines:
					status, err := ParseStatusReport(line)
					if err == nil && status.State == "Idle" {
						return cause
					}
				case err := <-readErrors:
					queryDeadline.Stop()
					return fmt.Errorf("%w; driver: jog stop was not confirmed: controller read failed: %v", cause, err)
				case <-queryDeadline.C:
					awaitingStatus = false
				case <-deadline.C:
					queryDeadline.Stop()
					return fmt.Errorf("%w; driver: jog stop was not confirmed within 2 seconds", cause)
				}
			}
			queryDeadline.Stop()
			select {
			case <-ticker.C:
			case <-deadline.C:
				return fmt.Errorf("%w; driver: jog stop was not confirmed within 2 seconds", cause)
			}
		}
	}
	awaitLine := func(deadline time.Duration) (string, error) {
		timer := time.NewTimer(deadline)
		defer timer.Stop()
		select {
		case line := <-lines:
			return line, nil
		case err := <-readErrors:
			return "", fmt.Errorf("driver: controller read failed: %w", err)
		case <-bounded.Done():
			return "", bounded.Err()
		case <-timer.C:
			return "", errors.New("driver: controller response timeout")
		}
	}
	readStatus := func(deadline time.Duration) (StatusReport, error) {
		end := time.Now().Add(deadline)
		for {
			remaining := time.Until(end)
			if remaining <= 0 {
				return StatusReport{}, errors.New("driver: controller status timeout")
			}
			line, err := awaitLine(remaining)
			if err != nil {
				return StatusReport{}, err
			}
			report, err := ParseStatusReport(line)
			if err == nil {
				return report, nil
			}
		}
	}
	if err := write("?"); err != nil {
		return fmt.Errorf("driver: query controller before jog: %w", err)
	}
	status, err := readStatus(manualResponse)
	if err != nil {
		return err
	}
	if status.State != "Idle" {
		return fmt.Errorf("driver: jogging requires an Idle controller; got %s", status.State)
	}
	if err := write("M5\n"); err != nil {
		return resetAfterAmbiguousWrite(fmt.Errorf("driver: turn laser off before jog: %w", err))
	}
	for {
		line, err := awaitLine(manualResponse)
		if err != nil {
			return err
		}
		if line == "ok" {
			break
		}
		if strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "ALARM:") {
			return fmt.Errorf("driver: controller refused laser-off command: %s", line)
		}
	}
	if err := write("?"); err != nil {
		return fmt.Errorf("driver: verify idle state before jog: %w", err)
	}
	status, err = readStatus(manualResponse)
	if err != nil {
		return err
	}
	if status.State != "Idle" {
		return fmt.Errorf("driver: jogging requires an Idle controller after laser-off; got %s", status.State)
	}
	command := fmt.Sprintf("$J=G91 G21 %s%s F%s\n", request.Axis, strconv.FormatFloat(request.DistanceMM, 'f', -1, 64), strconv.FormatFloat(request.FeedMMMin, 'f', -1, 64))
	if err := write(command); err != nil {
		return resetAfterAmbiguousWrite(fmt.Errorf("driver: send jog: %w", err))
	}
	acked := false
	statusTicker := time.NewTicker(manualPoll)
	defer statusTicker.Stop()
	for {
		select {
		case <-bounded.Done():
			return stopJog(bounded.Err())
		case <-statusTicker.C:
			if err := write("?"); err != nil {
				return stopJog(fmt.Errorf("driver: query jog state: %w", err))
			}
		case line := <-lines:
			if line == "ok" {
				acked = true
				continue
			}
			if strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "ALARM:") {
				return stopJog(fmt.Errorf("driver: jog rejected: %s", line))
			}
			status, parseErr := ParseStatusReport(line)
			if parseErr == nil && acked && status.State == "Idle" {
				return nil
			}
		case err := <-readErrors:
			return stopJog(fmt.Errorf("driver: controller read failed during jog: %w", err))
		}
	}
}

// ValidateJogRequest checks the operator-confirmation and motion bounds for a
// GRBL jog without opening or writing to a controller connection.
func ValidateJogRequest(request JogRequest) error {
	if !request.Confirmed {
		return errors.New("driver: operator confirmation is required before jogging")
	}
	if request.Axis != "X" && request.Axis != "Y" && request.Axis != "Z" {
		return errors.New("driver: jog axis must be X, Y, or Z")
	}
	if math.IsNaN(request.DistanceMM) || math.IsInf(request.DistanceMM, 0) || request.DistanceMM == 0 || math.Abs(request.DistanceMM) > MaxJogDistanceMM {
		return fmt.Errorf("driver: jog distance must be nonzero and no more than %.0f mm", MaxJogDistanceMM)
	}
	if math.IsNaN(request.FeedMMMin) || math.IsInf(request.FeedMMMin, 0) || request.FeedMMMin <= 0 || request.FeedMMMin > MaxJogFeedMMMin {
		return fmt.Errorf("driver: jog feed must be positive and no more than %.0f mm/min", MaxJogFeedMMMin)
	}
	return nil
}

// ManualCommandRequest describes a confirmed, bounded machine-preparation
// command. Frame bounds are absolute work coordinates and must be finite.
type ManualCommandRequest struct {
	Action    string
	Confirmed bool
	Bounds    toolpath.Bounds
}

// ManualCommand sends one preparation command after validating confirmation
// and controller state. Motion commands turn the laser off and verify Idle
// before issuing motion. Framing is stopped with feed hold and waits for a
// Hold:0 or Idle report if interrupted. Interrupted homing uses Ctrl-X, waits
// for Alarm, and reports that position is lost. The request context bounds
// status and acknowledgement reads as well as the motion. The connection is
// owned and closed by this call.
func ManualCommand(ctx context.Context, conn io.ReadWriteCloser, request ManualCommandRequest) error {
	if ctx == nil || conn == nil {
		return errors.New("driver: context and controller connection are required")
	}
	if !request.Confirmed {
		return errors.New("driver: operator confirmation is required for manual control")
	}
	command, moving, initialState, err := manualCommand(request)
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	lines := make(chan string, 16)
	readerDone := make(chan struct{})
	readerStop := make(chan struct{})
	readErrors := make(chan error, 1)
	go func() {
		defer close(readerDone)
		r := bufio.NewReaderSize(conn, 256)
		for {
			line, e := r.ReadSlice('\n')
			if e != nil {
				select {
				case readErrors <- e:
				default:
				}
				return
			}
			select {
			case lines <- strings.TrimSpace(string(line)):
			case <-readerStop:
				return
			}
		}
	}()
	defer func() { close(readerStop); _ = conn.Close(); <-readerDone }()
	write := func(raw string) error {
		if d, ok := conn.(interface{ SetWriteDeadline(time.Time) error }); ok {
			_ = d.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
		}
		timer := time.AfterFunc(100*time.Millisecond, func() { _ = conn.Close() })
		n, e := io.WriteString(conn, raw)
		timer.Stop()
		if e == nil && n != len(raw) {
			e = io.ErrShortWrite
		}
		return e
	}
	resetAfterAmbiguousWrite := func(cause error) error {
		return resetManualParser(write, lines, readErrors, cause)
	}
	read := func() (string, error) {
		select {
		case line := <-lines:
			return line, nil
		case e := <-readErrors:
			return "", e
		case <-bounded.Done():
			return "", bounded.Err()
		}
	}
	status := func() (StatusReport, error) {
		if e := write("?"); e != nil {
			return StatusReport{}, e
		}
		for {
			line, e := read()
			if e != nil {
				return StatusReport{}, e
			}
			report, e := ParseStatusReport(line)
			if e == nil {
				return report, nil
			}
		}
	}
	state, e := status()
	if e != nil {
		return e
	}
	if initialState == "Alarm" && !strings.HasPrefix(state.State, "Alarm") {
		return fmt.Errorf("driver: unlock requires Alarm; got %s", state.State)
	}
	if initialState == "Idle" && state.State != "Idle" {
		return fmt.Errorf("driver: manual command requires Idle; got %s", state.State)
	}
	if moving {
		if e = write("M5\n"); e != nil {
			return resetAfterAmbiguousWrite(e)
		}
		if e = awaitManualAck(read); e != nil {
			return e
		}
		state, e = status()
		if e != nil {
			return e
		}
		if state.State != "Idle" {
			return fmt.Errorf("driver: motion requires Idle after laser-off; got %s", state.State)
		}
	}
	motionSent := moving
	if e = write(command); e != nil {
		return resetAfterAmbiguousWrite(e)
	}
	ackCount := strings.Count(command, "\n")
	if e = awaitManualAcks(read, ackCount); e != nil {
		if motionSent {
			return stopManualMotion(request.Action, write, lines, readErrors, e)
		}
		return e
	}
	if request.Action == "unlock" {
		state, e = status()
		if e != nil {
			return e
		}
		if state.State != "Idle" {
			return fmt.Errorf("driver: unlock did not return controller to Idle; got %s", state.State)
		}
	}
	if moving {
		ticker := time.NewTicker(manualPoll)
		defer ticker.Stop()
		for {
			state, e = status()
			if e != nil {
				return stopManualMotion(request.Action, write, lines, readErrors, e)
			}
			if state.State == "Idle" {
				break
			}
			select {
			case <-ticker.C:
			case <-bounded.Done():
				return stopManualMotion(request.Action, write, lines, readErrors, bounded.Err())
			}
		}
	}
	return nil
}

// resetManualParser handles a G-code line whose write may have delivered only
// a prefix. Realtime stop and feed-hold leave GRBL's line parser buffer intact,
// so only a confirmed soft reset is safe before the controller is reused.
func resetManualParser(write func(string) error, lines <-chan string, readErrors <-chan error, cause error) error {
	unconfirmed := func(reason string) error {
		return fmt.Errorf("%w; driver: manual command parser reset was not confirmed: %s; inspect or restart the controller before releasing the lease", cause, reason)
	}
	if err := write("\x18"); err != nil {
		return unconfirmed(fmt.Sprintf("could not send Ctrl-X: %v", err))
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	bannerSeen := false
	for !bannerSeen {
		select {
		case line := <-lines:
			bannerSeen = strings.HasPrefix(line, "Grbl ")
		case err := <-readErrors:
			return unconfirmed(fmt.Sprintf("controller read failed while waiting for reset banner: %v", err))
		case <-deadline.C:
			return unconfirmed("reset banner did not arrive within 2 seconds")
		}
	}
	if err := write("?"); err != nil {
		return unconfirmed(fmt.Sprintf("could not query reset state: %v", err))
	}
	for {
		select {
		case line := <-lines:
			status, err := ParseStatusReport(line)
			if err != nil {
				continue
			}
			if status.State != "Alarm" && status.State != "Idle" {
				continue
			}
			return fmt.Errorf("%w; driver: parser reset cleared the partial command; controller state is %s; position may be lost; re-home before further motion", cause, status.State)
		case err := <-readErrors:
			return unconfirmed(fmt.Sprintf("controller read failed while confirming reset state: %v", err))
		case <-deadline.C:
			return unconfirmed("terminal Idle or Alarm status did not arrive within 2 seconds")
		}
	}
}

// stopManualMotion uses the controller-specific stop behavior and waits for a
// fresh terminal status before the manual command can report completion. GRBL
// 0x85 only cancels $J jogs; framing uses ordinary G0 moves and homing needs a
// reset to stop. A homing reset discards position and leaves GRBL in Alarm.
func stopManualMotion(action string, write func(string) error, lines <-chan string, readErrors <-chan error, cause error) error {
	stopCommand := "!"
	stopped := func(state string) bool { return state == "Idle" || state == "Hold:0" }
	if action == "home" {
		stopCommand = "\x18"
		stopped = func(state string) bool { return strings.HasPrefix(state, "Alarm") }
	}
	if err := write(stopCommand); err != nil {
		return fmt.Errorf("%w; driver: motion stop was not confirmed: could not send stop command: %v", cause, err)
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(manualPoll)
	defer ticker.Stop()
	for {
		if err := write("?"); err != nil {
			return fmt.Errorf("%w; driver: motion stop was not confirmed: status query failed: %v", cause, err)
		}
		queryDeadline := time.NewTimer(manualPoll)
		awaitingStatus := true
		for awaitingStatus {
			select {
			case line := <-lines:
				state, err := ParseStatusReport(line)
				if err == nil && stopped(state.State) {
					if action == "home" {
						return fmt.Errorf("%w; driver: homing was stopped by soft reset; position is lost and the controller remains in Alarm", cause)
					}
					return cause
				}
			case err := <-readErrors:
				queryDeadline.Stop()
				return fmt.Errorf("%w; driver: motion stop was not confirmed: controller read failed: %v", cause, err)
			case <-queryDeadline.C:
				awaitingStatus = false
			case <-deadline.C:
				queryDeadline.Stop()
				return fmt.Errorf("%w; driver: motion stop was not confirmed within 2 seconds", cause)
			}
		}
		queryDeadline.Stop()
		select {
		case <-ticker.C:
		case <-deadline.C:
			return fmt.Errorf("%w; driver: motion stop was not confirmed within 2 seconds", cause)
		}
	}
}

func awaitManualAck(read func() (string, error)) error {
	for {
		line, e := read()
		if e != nil {
			return e
		}
		if line == "ok" {
			return nil
		}
		if strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "ALARM:") {
			return fmt.Errorf("driver: controller rejected manual command: %s", line)
		}
	}
}

func awaitManualAcks(read func() (string, error), count int) error {
	for i := 0; i < count; i++ {
		if err := awaitManualAck(read); err != nil {
			return err
		}
	}
	return nil
}

func manualCommand(request ManualCommandRequest) (string, bool, string, error) {
	switch request.Action {
	case "home":
		return "$H\n", true, "Idle", nil
	case "unlock":
		return "$X\n", false, "Alarm", nil
	case "set_zero":
		return "G10 L20 P1 X0 Y0\n", false, "Idle", nil
	case "frame":
		b := request.Bounds
		values := []float64{b.Min.X, b.Min.Y, b.Max.X, b.Max.Y}
		for _, v := range values {
			if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 500 {
				return "", false, "", errors.New("driver: frame bounds must be finite and within 500 mm")
			}
		}
		if b.Min.X >= b.Max.X || b.Min.Y >= b.Max.Y {
			return "", false, "", errors.New("driver: frame bounds must have positive width and height")
		}
		// Use a bounded rectangle and rapid motion only after M5 and Idle checks.
		return fmt.Sprintf("G90 G21\nG0 X%s Y%s\nG0 X%s Y%s\nG0 X%s Y%s\nG0 X%s Y%s\n", strconv.FormatFloat(b.Min.X, 'f', -1, 64), strconv.FormatFloat(b.Min.Y, 'f', -1, 64), strconv.FormatFloat(b.Max.X, 'f', -1, 64), strconv.FormatFloat(b.Min.Y, 'f', -1, 64), strconv.FormatFloat(b.Max.X, 'f', -1, 64), strconv.FormatFloat(b.Max.Y, 'f', -1, 64), strconv.FormatFloat(b.Min.X, 'f', -1, 64), strconv.FormatFloat(b.Min.Y, 'f', -1, 64)), true, "Idle", nil
	default:
		return "", false, "", errors.New("driver: unsupported manual action")
	}
}
