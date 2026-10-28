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
	"sync/atomic"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
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
	JobID string `json:"job_id"`
	Lease string `json:"lease"`
	State string `json:"state"`
	Acked int    `json:"acked"`
	Total int    `json:"total"`
	Error string `json:"error,omitempty"`
}
type pollRequest struct {
	Session string `json:"session"`
	*progress
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
		err := post(ctx, client, endpoint, key, pollRequest{Session: session}, &assignment)
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
		case "run":
			if assignment.Job.ID == "" || assignment.Lease == "" {
				return errors.New("worker: server returned incomplete assignment")
			}
			if terminal, ok := completed[assignment.Lease]; ok {
				if err := postTerminal(ctx, client, endpoint, key, session, terminal); err != nil {
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
	var acked atomic.Int64
	var totalCount atomic.Int64
	totalCount.Store(int64(total))
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- driver.Stream(streamCtx, conn, a.Job.GCode, func(x, n int) { totalCount.Store(int64(n)); acked.Store(int64(x)) })
	}()
	ticker := time.NewTicker(reportInterval)
	defer ticker.Stop()
	for {
		select {
		case streamErr := <-streamDone:
			state := "completed"
			if streamErr != nil {
				if errors.Is(streamErr, context.Canceled) && ctx.Err() != nil {
					state = "canceled"
				} else {
					state = "failed"
				}
			}
			p := progress{JobID: a.Job.ID, Lease: a.Lease, State: state, Acked: int(acked.Load()), Total: int(totalCount.Load())}
			if streamErr != nil {
				p.Error = streamErr.Error()
			}
			return p, nil
		case <-ctx.Done():
			cancel()
			<-streamDone
			return progress{JobID: a.Job.ID, Lease: a.Lease, State: "canceled", Acked: int(acked.Load()), Total: int(totalCount.Load())}, nil
		case <-ticker.C:
			p := progress{JobID: a.Job.ID, Lease: a.Lease, State: "running", Acked: int(acked.Load()), Total: int(totalCount.Load())}
			var response assignment
			err := post(ctx, client, endpoint, key, pollRequest{Session: session, progress: &p}, &response)
			if err != nil {
				cancel()
				<-streamDone
				return progress{}, fmt.Errorf("worker: progress report: %w", err)
			}
			if response.Command == "cancel" {
				if response.Lease != a.Lease || response.Job.ID != a.Job.ID {
					cancel()
					<-streamDone
					return progress{}, errors.New("worker: cancel response changed active assignment")
				}
				cancel()
				<-streamDone
				return progress{JobID: a.Job.ID, Lease: a.Lease, State: "canceled", Acked: int(acked.Load()), Total: int(totalCount.Load())}, nil
			}
			if response.Command == "run" && (response.Lease != a.Lease || response.Job.ID != a.Job.ID) {
				cancel()
				<-streamDone
				return progress{}, errors.New("worker: server changed active assignment")
			}
			if response.Command != "run" {
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
		err := post(ctx, client, endpoint, key, pollRequest{Session: session, progress: &p}, &response)
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
