// Package simulator provides a deterministic example implementation of the
// public driveradapter contract. It is software-only and not machine evidence.
package simulator

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/ConstructorFleet/emberdeck/driveradapter"
)

type Adapter struct {
	mu             sync.Mutex
	manifest       driveradapter.Manifest
	deviceCaps     driveradapter.Capabilities
	startCount     int
	lastAssignment driveradapter.Assignment
}

func New() *Adapter {
	caps := driveradapter.Capabilities{PauseResume: true, Cancel: true, PositionFeedback: true}
	return &Adapter{
		manifest: driveradapter.Manifest{
			ProtocolVersion: driveradapter.ProtocolVersion,
			Name:            "emberdeck-simulator",
			Version:         "1.0.0",
			Transport:       "simulator",
			Capabilities:    caps,
		},
		deviceCaps: caps,
	}
}

func NewWithoutControls() *Adapter {
	a := New()
	a.manifest.Capabilities.PauseResume = false
	a.manifest.Capabilities.Cancel = false
	a.deviceCaps = a.manifest.Capabilities
	return a
}

func (a *Adapter) Manifest() driveradapter.Manifest { return a.manifest }

func (a *Adapter) Discover(ctx context.Context) ([]driveradapter.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []driveradapter.Device{{ID: "simulator-1", Name: "Software simulator", Capabilities: a.deviceCaps}}, nil
}

func (a *Adapter) Configure(ctx context.Context, deviceID string, config json.RawMessage) (driveradapter.ControllerSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if deviceID != "simulator-1" {
		return nil, errors.New("unknown simulator device")
	}
	if len(config) != 0 && !json.Valid(config) {
		return nil, errors.New("simulator configuration must be valid JSON")
	}
	return &session{adapter: a}, nil
}

func (a *Adapter) StartCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.startCount
}

type session struct {
	mu         sync.Mutex
	adapter    *Adapter
	started    bool
	closed     bool
	state      driveradapter.State
	assignment driveradapter.Assignment
}

func (s *session) Start(ctx context.Context, assignment driveradapter.Assignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed || s.started {
		return errors.New("simulator session is not startable")
	}
	s.started = true
	s.assignment = assignment
	s.state = driveradapter.StateRunning
	s.adapter.mu.Lock()
	s.adapter.startCount++
	s.adapter.lastAssignment = assignment
	s.adapter.mu.Unlock()
	return nil
}

func (s *session) Status(ctx context.Context) (driveradapter.Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return driveradapter.Status{}, err
	}
	if s.closed {
		return driveradapter.Status{}, errors.New("simulator session is closed")
	}
	if !s.started {
		return driveradapter.Status{State: driveradapter.StateIdle}, nil
	}
	lines := 0
	for _, line := range strings.Split(string(s.assignment.GCode), "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	executed := 0
	if lines > 0 {
		executed = lines - 1
	}
	var position *driveradapter.Position
	feedback := driveradapter.FeedbackUnavailable
	if s.adapter.deviceCaps.PositionFeedback {
		position = &driveradapter.Position{X: 1, Y: 2}
		feedback = driveradapter.FeedbackEstimated
	}
	return driveradapter.Status{
		State:            s.state,
		AcceptedLines:    lines,
		TotalLines:       lines,
		ExecutedLines:    &executed,
		ControllerState:  string(s.state),
		Position:         position,
		PositionFeedback: feedback,
	}, nil
}

func (s *session) Pause(ctx context.Context) error { return s.setState(ctx, driveradapter.StatePaused) }
func (s *session) Resume(ctx context.Context) error {
	return s.setState(ctx, driveradapter.StateRunning)
}
func (s *session) Cancel(ctx context.Context) error {
	return s.setState(ctx, driveradapter.StateFailed)
}

func (s *session) setState(ctx context.Context, state driveradapter.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.closed || !s.started {
		return errors.New("simulator session is not active")
	}
	s.state = state
	return nil
}

func (s *session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
