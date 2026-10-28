// Package worker polls EmberDeck for a job and streams it to one controller.
package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

const (
	pollInterval   = time.Second
	reportInterval = time.Second
	requestTimeout = 10 * time.Second
)

// Connector opens the worker's sole controller wire connection.
type Connector func(context.Context) (io.ReadWriteCloser, error)

type assignment struct {
	Command string `json:"command"`
	Lease   string `json:"lease"`
	Job     struct {
		ID    string `json:"id"`
		GCode string `json:"gcode"`
	} `json:"job"`
}
type progress struct {
	JobID           string              `json:"job_id"`
	Lease           string              `json:"lease"`
	State           string              `json:"state"`
	Acked           int                 `json:"acked"`
	Total           int                 `json:"total"`
	Error           string              `json:"error,omitempty"`
	ControllerState string              `json:"controller_state,omitempty"`
	Execution       *toolpath.Execution `json:"execution,omitempty"`
}
type pollRequest struct {
	Session      string   `json:"session"`
	Capabilities []string `json:"capabilities"`
	*progress
}

type liveState struct {
	mu            sync.Mutex
	desired       string
	controller    string
	stateSeq      uint64
	pauseApplied  bool
	pauseSeq      uint64
	resumeApplied bool
	resumeSeq     uint64
	externalHold  bool
	execution     toolpath.Execution
	tracker       *toolpath.Tracker
}

func newLiveState(state, gcode string) *liveState {
	parsed := toolpath.Parse(gcode)
	return &liveState{desired: state, tracker: toolpath.NewTracker(parsed), execution: toolpath.Execution{
		Progress: toolpath.Progress{Total: len(parsed.Segments), Confidence: "unknown"},
	}}
}

func (s *liveState) observed(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.controller = state
	s.stateSeq++
	if s.desired == "running" {
		switch state {
		case "Hold:1":
			s.desired = "pausing"
			s.externalHold = true
		case "Hold:0":
			s.desired = "paused"
			s.externalHold = true
		}
	} else if s.externalHold && (s.desired == "pausing" || s.desired == "paused") && state == "Hold:0" {
		s.desired = "paused"
	}
	if s.desired == "running" && state != "Hold:1" && state != "Hold:0" {
		s.externalHold = false
	}
}

func (s *liveState) applied(command driver.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch command {
	case driver.Pause:
		s.pauseApplied = true
		s.pauseSeq = s.stateSeq
	case driver.Resume:
		s.resumeApplied = true
		s.resumeSeq = s.stateSeq
	}
}

func (s *liveState) observedStatus(report driver.StatusReport) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if report.MachinePosition != nil {
		position := pathPosition(*report.MachinePosition)
		s.execution.MachinePosition = &position
	}
	if report.WorkPosition != nil {
		position := pathPosition(*report.WorkPosition)
		s.execution.WorkPosition = &position
		s.execution.PositionUnits = "assumed-mm"
	} else if report.MachinePosition != nil {
		s.execution.PositionUnits = "controller"
	}
	if report.WorkOffset != nil {
		offset := pathPosition(*report.WorkOffset)
		s.execution.WorkOffset = &offset
	}
	if report.LineNumber != nil {
		line := *report.LineNumber
		s.execution.LineNumber = &line
		s.execution.Progress = s.tracker.UpdateLineNumber(line)
	} else if report.WorkPosition != nil {
		s.execution.Progress = s.tracker.Update(toolpath.Point{X: report.WorkPosition.X, Y: report.WorkPosition.Y})
	} else {
		s.execution.Progress = s.tracker.Unknown()
	}
	if report.WorkPosition != nil || report.MachinePosition != nil || report.LineNumber != nil {
		s.execution.PositionReportedAt = time.Now().UTC()
	}
}

func pathPosition(position driver.Vector) toolpath.Position {
	return toolpath.Position{X: position.X, Y: position.Y, Z: position.Z, A: position.A}
}

func (s *liveState) uncertain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execution.Progress = s.tracker.Unknown()
}

func (s *liveState) finished() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execution.Progress = s.tracker.Finish()
}

func (s *liveState) reported(acked, total int) progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.desired
	switch state {
	case "pausing":
		if s.externalHold && s.controller == "Hold:0" || s.pauseApplied && s.stateSeq > s.pauseSeq && (s.controller == "Hold:0" || s.controller == "Idle") {
			state = "paused"
			s.desired = state
		}
	case "resuming":
		if s.resumeApplied && s.stateSeq > s.resumeSeq && (s.controller == "Run" || s.controller == "Idle") {
			state = "running"
			s.desired = state
			s.externalHold = false
		}
	}
	execution := cloneExecution(s.execution)
	return progress{State: state, Acked: acked, Total: total, ControllerState: s.controller, Execution: &execution}
}

func cloneExecution(source toolpath.Execution) toolpath.Execution {
	copyPosition := func(source *toolpath.Position) *toolpath.Position {
		if source == nil {
			return nil
		}
		position := *source
		return &position
	}
	result := source
	result.MachinePosition = copyPosition(source.MachinePosition)
	result.WorkPosition = copyPosition(source.WorkPosition)
	result.WorkOffset = copyPosition(source.WorkOffset)
	if source.LineNumber != nil {
		line := *source.LineNumber
		result.LineNumber = &line
	}
	if source.Progress.Active != nil {
		active := *source.Progress.Active
		result.Progress.Active = &active
	}
	return result
}

// Run polls the server using one unique session per process. It returns on
// cancellation or any poll/protocol error. A failed poll is fail-closed: an
// active stream is canceled and no further job commands are sent.
func Run(ctx context.Context, serverURL, key, id string, connect Connector) error {
	if ctx == nil || connect == nil || strings.TrimSpace(serverURL) == "" || key == "" || id == "" {
		return errors.New("worker: context, server URL, key, driver ID, and connector are required")
	}
	session, err := newSession()
	if err != nil {
		return fmt.Errorf("worker: create session: %w", err)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/drivers/" + id + "/poll"
	client := &http.Client{Timeout: requestTimeout}
	completed := make(map[string]progress)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var assignment assignment
		err := post(ctx, client, endpoint, key, pollRequest{Session: session, Capabilities: []string{"pause_resume"}}, &assignment)
		if err != nil {
			return fmt.Errorf("worker: poll: %w", err)
		}
		switch assignment.Command {
		case "idle":
			if err := wait(ctx, pollInterval); err != nil {
				return err
			}
		case "cancel":
			if assignment.Job.ID == "" || assignment.Lease == "" {
				return errors.New("worker: cancel command has no assignment")
			}
			terminal := progress{JobID: assignment.Job.ID, Lease: assignment.Lease, State: "canceled"}
			completed[assignment.Lease] = terminal
			if err := postTerminal(ctx, client, endpoint, key, session, terminal); err != nil {
				return err
			}
		case "run", "pause", "resume":
			if assignment.Job.ID == "" || assignment.Lease == "" {
				return errors.New("worker: server returned incomplete assignment")
			}
			if terminal, ok := completed[assignment.Lease]; ok {
				if err := postTerminal(ctx, client, endpoint, key, session, terminal); err != nil {
					return err
				}
				if err := wait(ctx, pollInterval); err != nil {
					return err
				}
				continue
			}
			terminal, err := runJob(ctx, client, endpoint, key, session, assignment, connect)
			if err != nil && terminal.State == "" {
				return err
			}
			completed[assignment.Lease] = terminal
			if postErr := postTerminal(ctx, client, endpoint, key, session, terminal); postErr != nil {
				return postErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		default:
			return fmt.Errorf("worker: unknown server command %q", assignment.Command)
		}
	}
}

func runJob(ctx context.Context, client *http.Client, endpoint, key, session string, a assignment, connect Connector) (progress, error) {
	total, validationErr := driver.Validate(a.Job.GCode)
	if validationErr != nil {
		return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Error: validationErr.Error()}, nil
	}
	conn, err := connect(ctx)
	if err != nil {
		return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Error: err.Error()}, nil
	}
	if conn == nil {
		return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Error: "connector returned nil connection"}, nil
	}
	defer conn.Close()
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	commands := make(chan driver.Command, 4)
	state := "running"
	if a.Command == "pause" {
		commands <- driver.Pause
		state = "pausing"
	}
	live := newLiveState(state, a.Job.GCode)
	var acked atomic.Int64
	var totalCount atomic.Int64
	totalCount.Store(int64(total))
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- driver.StreamObserved(streamCtx, conn, a.Job.GCode, commands, func(x, n int) { totalCount.Store(int64(n)); acked.Store(int64(x)) }, live.observed, live.observedStatus, live.applied)
	}()
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()
	for {
		select {
		case streamErr := <-streamDone:
			state := "completed"
			if streamErr != nil {
				live.uncertain()
				if ctx.Err() != nil {
					state = "canceled"
				} else {
					state = "failed"
				}
			} else {
				live.finished()
			}
			p := live.reported(int(acked.Load()), int(totalCount.Load()))
			p.JobID, p.Lease, p.State = a.Job.ID, a.Lease, state
			if streamErr != nil {
				p.Error = streamErr.Error()
			}
			return p, nil
		case <-ctx.Done():
			cancel()
			<-streamDone
			live.uncertain()
			p := live.reported(int(acked.Load()), int(totalCount.Load()))
			p.JobID, p.Lease, p.State = a.Job.ID, a.Lease, "canceled"
			return p, nil
		case <-ticker.C:
			p := live.reported(int(acked.Load()), int(totalCount.Load()))
			p.JobID, p.Lease = a.Job.ID, a.Lease
			var response assignment
			err := post(ctx, client, endpoint, key, pollRequest{Session: session, Capabilities: []string{"pause_resume"}, progress: &p}, &response)
			if err != nil {
				cancel()
				<-streamDone
				return progress{}, fmt.Errorf("worker: progress report: %w", err)
			}
			if response.Lease != a.Lease || response.Job.ID != a.Job.ID {
				cancel()
				<-streamDone
				return progress{}, errors.New("worker: server changed active assignment")
			}
			if response.Command == "cancel" || response.Command == "pause" || response.Command == "resume" || response.Command == "run" {
				switch response.Command {
				case "cancel":
					cancel()
					<-streamDone
					live.uncertain()
					p := live.reported(int(acked.Load()), int(totalCount.Load()))
					p.JobID, p.Lease, p.State = a.Job.ID, a.Lease, "canceled"
					return p, nil
				case "pause":
					sendPause := false
					live.mu.Lock()
					if live.desired != "pausing" && live.desired != "paused" {
						live.desired = "pausing"
						live.pauseApplied = false
						sendPause = true
					}
					live.mu.Unlock()
					if sendPause {
						commands <- driver.Pause
					}
				case "resume":
					sendResume := false
					live.mu.Lock()
					if live.desired != "resuming" {
						live.resumeApplied = false
						live.desired = "resuming"
						sendResume = true
					}
					live.mu.Unlock()
					if sendResume {
						commands <- driver.Resume
					}
				case "run":
					// The run command never emits GRBL's resume byte.
				}
			} else {
				cancel()
				<-streamDone
				return progress{}, fmt.Errorf("worker: unexpected progress command %q", response.Command)
			}
		}
	}
}

func postTerminal(ctx context.Context, client *http.Client, endpoint, key, session string, p progress) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var response assignment
		err := post(ctx, client, endpoint, key, pollRequest{Session: session, Capabilities: []string{"pause_resume"}, progress: &p}, &response)
		if err == nil {
			return nil
		}
		var statusErr *statusError
		if errors.As(err, &statusErr) && statusErr.code < 500 {
			return fmt.Errorf("worker: terminal report rejected: %w", err)
		}
		if err := wait(ctx, time.Second); err != nil {
			return err
		}
	}
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.code, e.body) }
func post(ctx context.Context, client *http.Client, endpoint, key string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &statusError{response.StatusCode, strings.TrimSpace(string(data))}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("invalid server response: %w", err)
	}
	return nil
}
func newSession() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
