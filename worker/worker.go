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
	"log"
	mathrand "math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/jobtransfer"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

const (
	pollInterval   = time.Second
	reportInterval = time.Second
	requestTimeout = 10 * time.Second

	defaultRetryMin = time.Second
	defaultRetryMax = 30 * time.Second
)

// Options tunes how a worker reconnects. The zero value uses production
// defaults.
type Options struct {
	// RetryMin is the base delay before the first retry after a failed idle
	// poll. Each further failure doubles it up to RetryMax. The actual delay
	// is jittered uniformly between half and all of the base. Default 1s.
	RetryMin time.Duration
	// RetryMax caps the retry base delay. Default 30s.
	RetryMax time.Duration
	// Logf receives one line per connection, job start and outcome, retry,
	// reconnection, and supervised restart.
	// Default log.Printf.
	Logf func(format string, args ...any)
}

func (o Options) normalized() Options {
	if o.RetryMin <= 0 {
		o.RetryMin = defaultRetryMin
	}
	if o.RetryMax <= 0 {
		o.RetryMax = defaultRetryMax
	}
	if o.RetryMax < o.RetryMin {
		o.RetryMax = o.RetryMin
	}
	if o.Logf == nil {
		o.Logf = log.Printf
	}
	return o
}

// backoff doubles a base delay per failure and returns it with equal jitter,
// so restarted workers do not reconnect in lockstep.
type backoff struct {
	min, max, base time.Duration
}

func (b *backoff) next() time.Duration {
	if b.base < b.min {
		b.base = b.min
	}
	base := b.base
	b.base = min(base*2, b.max)
	half := base / 2
	return half + mathrand.N(base-half+1)
}

func (b *backoff) reset() { b.base = 0 }

// permanent reports poll failures that a retry cannot fix: a malformed
// request, a rejected API key, or a forbidden driver ID.
func permanent(err error) bool {
	var statusErr *statusError
	return errors.As(err, &statusErr) && (statusErr.code == http.StatusBadRequest || statusErr.code == http.StatusUnauthorized || statusErr.code == http.StatusForbidden)
}

// Connector opens the worker's sole controller wire connection.
type Connector func(context.Context) (io.ReadWriteCloser, error)

type assignment struct {
	Command string `json:"command"`
	Lease   string `json:"lease"`
	// ReadSettings asks an idle worker to probe the controller with `$$`.
	ReadSettings    bool              `json:"read_settings"`
	ReadStatus      bool              `json:"read_status"`
	ReadParserReset bool              `json:"read_parser_reset"`
	Manual          *manualAssignment `json:"manual,omitempty"`
	Job             struct {
		ID      string                `json:"id"`
		GCode   string                `json:"gcode"`
		Total   int                   `json:"total"`
		Size    int64                 `json:"size"`
		Content *jobtransfer.Manifest `json:"content"`
		// ExpectedSettings is the job's profile snapshot. When present, the
		// controller's `$$` values must match before any program byte is sent.
		ExpectedSettings *driver.Settings `json:"expected_settings"`
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
	Session                    string           `json:"session"`
	Capabilities               []string         `json:"capabilities"`
	ControllerSettings         *driver.Settings `json:"controller_settings,omitempty"`
	SettingsReadAt             *time.Time       `json:"settings_read_at,omitempty"`
	SettingsError              string           `json:"settings_error,omitempty"`
	ControllerState            string           `json:"controller_state,omitempty"`
	ControllerStatusFresh      bool             `json:"controller_status_fresh,omitempty"`
	ControllerParserResetFresh bool             `json:"controller_parser_reset_fresh,omitempty"`
	ManualLease                string           `json:"manual_lease,omitempty"`
	ManualState                string           `json:"manual_state,omitempty"`
	ManualError                string           `json:"manual_error,omitempty"`
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

// Run is RunWithOptions with default Options.
func Run(ctx context.Context, serverURL, key, id string, connect Connector) error {
	return RunWithOptions(ctx, serverURL, key, id, connect, Options{})
}

// RunWithOptions polls the server with one unique session at a time.
//
// While idle, a failed poll (network error, server restart, proxy error, or a
// 409 session conflict left by a previous process) is retried with jittered
// exponential backoff and a fresh session; each attempt is logged. HTTP 400,
// 401, and 403 are configuration errors and are returned.
//
// While holding an active lease the worker fails closed: a failed progress
// report cancels the stream, closes the controller, and returns an error
// without further job commands. It also returns on cancellation or a protocol
// error.
func RunWithOptions(ctx context.Context, serverURL, key, id string, connect Connector, options Options) error {
	if err := validArguments(ctx, serverURL, key, id, connect); err != nil {
		return err
	}
	return run(ctx, serverURL, key, id, connect, nil, options)
}

// RunWithAdapterRegistry polls the same lease-owning HTTP API as RunWithOptions,
// routing jobs for id through an explicitly registered in-process adapter.
func RunWithAdapterRegistry(ctx context.Context, serverURL, key, id string, registry *AdapterRegistry, options Options) error {
	if ctx == nil || strings.TrimSpace(serverURL) == "" || id == "" || registry == nil {
		return errors.New("worker: context, server URL, driver ID, and adapter registry are required")
	}
	registration, ok := registry.lookup(id)
	if !ok {
		return fmt.Errorf("worker: no adapter registered for driver %q", id)
	}
	return run(ctx, serverURL, key, id, nil, &registration, options)
}

func run(ctx context.Context, serverURL, key, id string, connect Connector, registration *adapterRegistration, options Options) error {
	options = options.normalized()
	session, err := newSession()
	if err != nil {
		return fmt.Errorf("worker: create session: %w", err)
	}
	endpoint := strings.TrimRight(serverURL, "/") + "/api/v1/drivers/" + id + "/poll"
	client := &http.Client{Timeout: requestTimeout}
	completed := make(map[string]progress)
	retry := backoff{min: options.RetryMin, max: options.RetryMax}
	failures := 0
	connected := false
	report := &controllerReport{}
	if registration != nil {
		report.setCapabilities(registration.capabilities)
	} else {
		report.enableManualCapabilities()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var assignment assignment
		err := post(ctx, client, endpoint, key, report.poll(session, nil), &assignment)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if permanent(err) {
				return fmt.Errorf("worker: poll: %w", err)
			}
			failures++
			delay := retry.next()
			options.Logf("worker %s: idle poll failed (attempt %d): %v; retrying with a new session in %s", id, failures, err, delay.Round(time.Millisecond))
			if err := wait(ctx, delay); err != nil {
				return err
			}
			if session, err = newSession(); err != nil {
				return fmt.Errorf("worker: create session: %w", err)
			}
			continue
		}
		if !connected {
			options.Logf("worker %s: connected to %s", id, serverURL)
			connected = true
		}
		if failures > 0 {
			options.Logf("worker %s: reconnected after %d failed polls", id, failures)
			failures = 0
			retry.reset()
		}
		switch assignment.Command {
		case "idle":
			if assignment.ReadParserReset {
				if registration != nil {
					return errors.New("worker: parser reset recovery is unsupported by the registered adapter")
				}
				conn, err := connect(ctx)
				if err == nil && conn == nil {
					err = errors.New("connector returned nil connection")
				}
				if err == nil {
					var status driver.StatusReport
					status, err = driver.ResetParser(ctx, conn, 5*time.Second)
					_ = conn.Close()
					if err == nil {
						report.setParserResetFresh(status.State)
					}
				}
				if err != nil {
					options.Logf("worker %s: controller parser reset remains unconfirmed: %v", id, err)
					if err := wait(ctx, pollInterval); err != nil {
						return err
					}
				}
				continue
			}
			if assignment.ReadStatus && registration == nil {
				report.probeStatus(ctx, connect)
				if ctx.Err() != nil {
					return ctx.Err()
				}
				continue
			}
			if assignment.ReadSettings {
				if registration != nil {
					report.failed(errors.New("controller settings probe is unsupported by the registered adapter"))
				} else {
					report.probe(ctx, connect)
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Report the probe result on the next poll without waiting.
				continue
			}
			if err := wait(ctx, pollInterval); err != nil {
				return err
			}
		case "manual":
			if registration != nil {
				return errors.New("worker: manual assignment is unsupported by this adapter")
			}
			if assignment.Lease == "" || assignment.Manual == nil {
				return errors.New("worker: server returned incomplete manual assignment")
			}
			if err := runManualLease(ctx, client, endpoint, key, session, connect, assignment, report); err != nil {
				return err
			}
		case "manual_running", "manual_cancel":
			return fmt.Errorf("worker: received %s without owning a manual lease", assignment.Command)
		case "cancel":
			if assignment.Job.ID == "" || assignment.Lease == "" {
				return errors.New("worker: cancel command has no assignment")
			}
			terminal := progress{JobID: assignment.Job.ID, Lease: assignment.Lease, State: "canceled", Total: assignment.Job.Total}
			completed[assignment.Lease] = terminal
			options.Logf("worker %s: job %s canceled before streaming", id, assignment.Job.ID)
			if err := postTerminal(ctx, client, endpoint, key, report.poll(session, &terminal)); err != nil {
				return err
			}
		case "run", "pause", "resume":
			if assignment.Job.ID == "" || assignment.Lease == "" {
				return errors.New("worker: server returned incomplete assignment")
			}
			if terminal, ok := completed[assignment.Lease]; ok {
				if err := postTerminal(ctx, client, endpoint, key, report.poll(session, &terminal)); err != nil {
					return err
				}
				if err := wait(ctx, pollInterval); err != nil {
					return err
				}
				continue
			}
			var terminal progress
			if reset := report.takeReset(); reset != "" {
				// A settings probe saw a reset banner; refuse without reopening.
				terminal = progress{JobID: assignment.Job.ID, Lease: assignment.Lease, State: "failed", Total: assignment.Job.Total, Error: reset}
			} else {
				options.Logf("worker %s: running job %s", id, assignment.Job.ID)
				if assignment.Job.Content != nil {
					gcode, fetchErr := fetchContent(ctx, client, endpoint, key, session, assignment)
					if fetchErr != nil {
						if ctx.Err() != nil {
							return ctx.Err()
						}
						terminal = progress{JobID: assignment.Job.ID, Lease: assignment.Lease, State: "failed", Total: assignment.Job.Total, Error: "job content transfer: " + fetchErr.Error()}
					} else {
						assignment.Job.GCode = gcode
					}
				}
				if terminal.State == "" {
					if registration != nil {
						terminal, err = runAdapterJob(ctx, client, endpoint, key, session, assignment, *registration, report)
					} else {
						terminal, err = runJob(ctx, client, endpoint, key, session, assignment, connect, report)
					}
					if err != nil && terminal.State == "" {
						return err
					}
				}
			}
			completed[assignment.Lease] = terminal
			if terminal.Error != "" {
				options.Logf("worker %s: job %s %s: %s", id, assignment.Job.ID, terminal.State, terminal.Error)
			} else {
				options.Logf("worker %s: job %s %s", id, assignment.Job.ID, terminal.State)
			}
			if postErr := postTerminal(ctx, client, endpoint, key, report.poll(session, &terminal)); postErr != nil {
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

// Supervise runs the worker until ctx ends. When the worker stops with an
// error, including a fail-closed exit from an active lease, the failure is
// logged and the worker restarts with a new session after jittered backoff.
// The server marks the driver offline and interrupts its active job when the
// old session's heartbeat expires; restarted workers never replay a lease.
func Supervise(ctx context.Context, serverURL, key, id string, connect Connector, options Options) error {
	if err := validArguments(ctx, serverURL, key, id, connect); err != nil {
		return err
	}
	options = options.normalized()
	retry := backoff{min: options.RetryMin, max: options.RetryMax}
	for {
		started := time.Now()
		err := RunWithOptions(ctx, serverURL, key, id, connect, options)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Since(started) > options.RetryMax {
			retry.reset()
		}
		delay := retry.next()
		options.Logf("worker %s: stopped: %v; driver offline, restarting with a new session in %s", id, err, delay.Round(time.Millisecond))
		if err := wait(ctx, delay); err != nil {
			return err
		}
	}
}

func validArguments(ctx context.Context, serverURL, key, id string, connect Connector) error {
	if ctx == nil || connect == nil || strings.TrimSpace(serverURL) == "" || id == "" {
		return errors.New("worker: context, server URL, driver ID, and connector are required")
	}
	return nil
}

// setAuthorization omits the header when no key is configured, matching a
// server that has authentication disabled.
func setAuthorization(req *http.Request, key string) {
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

func runJob(ctx context.Context, client *http.Client, endpoint, key, session string, a assignment, connect Connector, report *controllerReport) (progress, error) {
	if len(a.Job.GCode) > jobtransfer.MaxProgramBytes {
		return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Total: a.Job.Total, Error: "job program exceeds the 4 MiB worker limit"}, nil
	}
	total, validationErr := driver.Validate(a.Job.GCode)
	if validationErr != nil {
		return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Total: a.Job.Total, Error: validationErr.Error()}, nil
	}
	conn, err := connect(ctx)
	if err != nil {
		return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Total: total, Error: err.Error()}, nil
	}
	if conn == nil {
		return progress{JobID: a.Job.ID, Lease: a.Lease, State: "failed", Total: total, Error: "connector returned nil connection"}, nil
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
		streamDone <- driver.StreamWithOptions(streamCtx, conn, a.Job.GCode, driver.Options{
			Commands:   commands,
			OnProgress: func(x, n int) { totalCount.Store(int64(n)); acked.Store(int64(x)) },
			OnState:    live.observed,
			OnStatus:   live.observedStatus,
			OnApplied:  live.applied,
			OnSettings: func(settings driver.Settings) error { return report.verify(settings, a.Job.ExpectedSettings) },
		})
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
			err := post(ctx, client, endpoint, key, report.poll(session, &p), &response)
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

func postTerminal(ctx context.Context, client *http.Client, endpoint, key string, request pollRequest) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var response assignment
		err := post(ctx, client, endpoint, key, request, &response)
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

const (
	contentSessionHeader = "X-EmberDeck-Session"
	contentLeaseHeader   = "X-EmberDeck-Lease"
	contentDigestHeader  = "X-EmberDeck-Content-SHA256"
	contentFetchAttempts = 3
)

func fetchContent(ctx context.Context, client *http.Client, pollEndpoint, key, session string, a assignment) (string, error) {
	manifest := a.Job.Content
	if manifest == nil || a.Job.GCode != "" || a.Job.ID == "" || a.Job.Size != manifest.Size || a.Lease == "" || session == "" {
		return "", errors.New("incomplete or ambiguous content assignment")
	}
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	if _, err := hex.DecodeString(a.Job.ID); err != nil || len(a.Job.ID) != 32 {
		return "", errors.New("invalid job ID in content assignment")
	}
	contentEndpoint := strings.TrimSuffix(pollEndpoint, "/poll") + "/jobs/" + url.PathEscape(a.Job.ID) + "/content"
	transferClient := *client
	transferClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	content := make([]byte, 0, int(manifest.Size))
	for offset := int64(0); offset < manifest.Size; {
		length := min(int64(jobtransfer.MaxChunkBytes), manifest.Size-offset)
		chunk, err := fetchContentChunk(ctx, &transferClient, contentEndpoint, key, session, a.Lease, *manifest, offset, length)
		if err != nil {
			return "", err
		}
		content = append(content, chunk...)
		offset += int64(len(chunk))
	}
	if err := jobtransfer.Verify(content, *manifest); err != nil {
		return "", err
	}
	return string(content), nil
}

func fetchContentChunk(ctx context.Context, client *http.Client, endpoint, key, session, lease string, manifest jobtransfer.Manifest, start, length int64) ([]byte, error) {
	end := start + length - 1
	expectedRange := fmt.Sprintf("bytes %d-%d/%d", start, end, manifest.Size)
	for attempt := 0; attempt < contentFetchAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		setAuthorization(req, key)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		req.Header.Set(contentSessionHeader, session)
		req.Header.Set(contentLeaseHeader, lease)
		response, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if attempt+1 == contentFetchAttempts {
				return nil, fmt.Errorf("request chunk at byte %d: %w", start, err)
			}
			if err := wait(ctx, time.Duration(100*(1<<attempt))*time.Millisecond); err != nil {
				return nil, err
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, length+1))
		closeErr := response.Body.Close()
		if response.StatusCode >= http.StatusInternalServerError {
			if attempt+1 == contentFetchAttempts {
				return nil, fmt.Errorf("chunk request returned HTTP %d", response.StatusCode)
			}
			if err := wait(ctx, time.Duration(100*(1<<attempt))*time.Millisecond); err != nil {
				return nil, err
			}
			continue
		}
		if response.StatusCode != http.StatusPartialContent {
			return nil, fmt.Errorf("chunk request returned HTTP %d", response.StatusCode)
		}
		if readErr != nil || closeErr != nil || int64(len(data)) != length {
			if attempt+1 == contentFetchAttempts {
				return nil, fmt.Errorf("chunk at byte %d was truncated", start)
			}
			if err := wait(ctx, time.Duration(100*(1<<attempt))*time.Millisecond); err != nil {
				return nil, err
			}
			continue
		}
		mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/octet-stream" || response.Header.Get("Content-Range") != expectedRange || response.Header.Get(contentDigestHeader) != manifest.SHA256 {
			return nil, fmt.Errorf("chunk at byte %d does not match the content manifest", start)
		}
		return data, nil
	}
	return nil, errors.New("job content transfer failed")
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
	setAuthorization(req, key)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, jobtransfer.MaxWorkerResponseBytes))
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
