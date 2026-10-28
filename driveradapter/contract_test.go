package driveradapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/ConstructorFleet/emberdeck/driveradapter"
	"github.com/ConstructorFleet/emberdeck/driveradapter/simulator"
)

func TestConfiguredAdapterValidatesAndExecutesOneLease(t *testing.T) {
	adapter := simulator.New()
	devices, err := driveradapter.Discover(context.Background(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != "simulator-1" {
		t.Fatalf("devices = %#v", devices)
	}
	session, err := driveradapter.Configure(context.Background(), adapter, devices[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	assignment := driveradapter.NewAssignment("job-1", "lease-1", []byte("G1 X10 F300\nM5\n"))
	bad := assignment
	bad.SHA256 = "00"
	if err := session.Start(context.Background(), bad); !errors.Is(err, driveradapter.ErrInvalidAssignment) {
		t.Fatalf("invalid digest error = %v", err)
	}
	if got := adapter.StartCount(); got != 0 {
		t.Fatalf("invalid assignment reached adapter Start %d times", got)
	}
	if err := session.Start(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	if err := session.Start(context.Background(), assignment); !errors.Is(err, driveradapter.ErrAlreadyStarted) {
		t.Fatalf("second start error = %v", err)
	}
	status, err := session.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != driveradapter.StateRunning || status.AcceptedLines != 2 || status.ExecutedLines == nil || *status.ExecutedLines != 1 {
		t.Fatalf("status = %+v", status)
	}
	if err := session.Pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err = session.Status(context.Background())
	if err != nil || status.State != driveradapter.StatePaused {
		t.Fatalf("paused status = %+v, error = %v", status, err)
	}
	if err := session.Resume(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilitiesGatePauseAndResume(t *testing.T) {
	adapter := simulator.NewWithoutControls()
	devices, err := driveradapter.Discover(context.Background(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	session, err := driveradapter.Configure(context.Background(), adapter, devices[0].ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	assignment := driveradapter.NewAssignment("job-2", "lease-2", []byte("G1 X1\n"))
	if err := session.Start(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	status, err := session.Status(context.Background())
	if err != nil || status.Position == nil || status.PositionFeedback != driveradapter.FeedbackEstimated {
		t.Fatalf("declared position feedback missing: status=%+v error=%v", status, err)
	}
	if err := session.Pause(context.Background()); !errors.Is(err, driveradapter.ErrUnsupported) {
		t.Fatalf("pause without capability error = %v", err)
	}
	if err := session.Cancel(context.Background()); !errors.Is(err, driveradapter.ErrUnsupported) {
		t.Fatalf("cancel without capability error = %v", err)
	}
}

func TestConfigureRejectsMalformedJSONBeforeAdapterCall(t *testing.T) {
	adapter := simulator.New()
	_, err := driveradapter.Configure(context.Background(), adapter, "simulator-1", []byte(`{"device":`))
	if !errors.Is(err, driveradapter.ErrProtocol) {
		t.Fatalf("malformed configuration error = %v", err)
	}
}

type unsupportedVersionAdapter struct {
	*simulator.Adapter
}

func (a unsupportedVersionAdapter) Manifest() driveradapter.Manifest {
	manifest := a.Adapter.Manifest()
	manifest.ProtocolVersion++
	return manifest
}

func TestDiscoverRejectsUnsupportedProtocolVersion(t *testing.T) {
	adapter := unsupportedVersionAdapter{Adapter: simulator.New()}
	if _, err := driveradapter.Discover(context.Background(), adapter); !errors.Is(err, driveradapter.ErrProtocol) {
		t.Fatalf("unsupported protocol version error = %v", err)
	}
}

type scriptedAdapter struct {
	statuses []driveradapter.Status
	statusAt int
}

func (a *scriptedAdapter) Manifest() driveradapter.Manifest {
	return driveradapter.Manifest{
		ProtocolVersion: driveradapter.ProtocolVersion,
		Name:            "scripted",
		Version:         "1.0.0",
		Transport:       "test",
		Capabilities:    driveradapter.Capabilities{PositionFeedback: true},
	}
}

func (a *scriptedAdapter) Discover(context.Context) ([]driveradapter.Device, error) {
	return []driveradapter.Device{{
		ID:           "device-1",
		Name:         "Scripted device",
		Capabilities: driveradapter.Capabilities{PositionFeedback: true},
	}}, nil
}

func (a *scriptedAdapter) Configure(context.Context, string, json.RawMessage) (driveradapter.ControllerSession, error) {
	return &scriptedController{adapter: a}, nil
}

type scriptedController struct{ adapter *scriptedAdapter }

func (*scriptedController) Start(context.Context, driveradapter.Assignment) error { return nil }
func (s *scriptedController) Status(context.Context) (driveradapter.Status, error) {
	if s.adapter.statusAt >= len(s.adapter.statuses) {
		return driveradapter.Status{}, errors.New("no scripted status")
	}
	status := s.adapter.statuses[s.adapter.statusAt]
	s.adapter.statusAt++
	return status, nil
}
func (*scriptedController) Pause(context.Context) error  { return nil }
func (*scriptedController) Resume(context.Context) error { return nil }
func (*scriptedController) Cancel(context.Context) error { return nil }
func (*scriptedController) Close() error                 { return nil }

func newScriptedSession(t *testing.T, statuses ...driveradapter.Status) *driveradapter.Session {
	t.Helper()
	adapter := &scriptedAdapter{statuses: statuses}
	session, err := driveradapter.Configure(context.Background(), adapter, "device-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	assignment := driveradapter.NewAssignment("job-1", "lease-1", []byte("G1 X1\n"))
	if err := session.Start(context.Background(), assignment); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestStatusRejectsNonFinitePosition(t *testing.T) {
	for _, test := range []struct {
		name     string
		position driveradapter.Position
	}{
		{name: "x NaN", position: driveradapter.Position{X: math.NaN(), Y: 1}},
		{name: "y positive infinity", position: driveradapter.Position{X: 1, Y: math.Inf(1)}},
		{name: "z negative infinity", position: driveradapter.Position{X: 1, Y: 2, Z: math.Inf(-1)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newScriptedSession(t, driveradapter.Status{
				State: driveradapter.StateRunning, AcceptedLines: 1, TotalLines: 1,
				ExecutedLines: intPointer(1), Position: &test.position,
				PositionFeedback: driveradapter.FeedbackConfirmed,
			})
			if _, err := session.Status(context.Background()); !errors.Is(err, driveradapter.ErrProtocol) {
				t.Fatalf("non-finite position error = %v", err)
			}
		})
	}
}

func TestStatusRequiresPositionForConfirmedOrEstimatedFeedback(t *testing.T) {
	for _, confidence := range []driveradapter.Feedback{
		driveradapter.FeedbackConfirmed,
		driveradapter.FeedbackEstimated,
	} {
		t.Run(string(confidence), func(t *testing.T) {
			session := newScriptedSession(t, driveradapter.Status{
				State: driveradapter.StateRunning, AcceptedLines: 1, TotalLines: 1,
				ExecutedLines: intPointer(1), Position: nil, PositionFeedback: confidence,
			})
			if _, err := session.Status(context.Background()); !errors.Is(err, driveradapter.ErrProtocol) {
				t.Fatalf("%s without a position error = %v", confidence, err)
			}
		})
	}
}

func TestCompletedStatusRequiresCompleteAcceptedAndReportedExecutedLines(t *testing.T) {
	for _, test := range []struct {
		name   string
		status driveradapter.Status
	}{
		{
			name: "accepted lines incomplete",
			status: driveradapter.Status{
				State: driveradapter.StateCompleted, AcceptedLines: 1, TotalLines: 2,
				ExecutedLines: intPointer(1), PositionFeedback: driveradapter.FeedbackUnavailable,
			},
		},
		{
			name: "reported executed lines incomplete",
			status: driveradapter.Status{
				State: driveradapter.StateCompleted, AcceptedLines: 2, TotalLines: 2,
				ExecutedLines: intPointer(1), PositionFeedback: driveradapter.FeedbackUnavailable,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := newScriptedSession(t, test.status)
			if _, err := session.Status(context.Background()); !errors.Is(err, driveradapter.ErrProtocol) {
				t.Fatalf("incomplete completed status error = %v", err)
			}
		})
	}
}

func TestRejectedStatusDoesNotAdvanceMonotonicProgress(t *testing.T) {
	session := newScriptedSession(t,
		driveradapter.Status{
			State: driveradapter.StateRunning, AcceptedLines: 1, TotalLines: 3,
			ExecutedLines: intPointer(1), PositionFeedback: driveradapter.FeedbackUnavailable,
		},
		driveradapter.Status{
			State: driveradapter.StateCompleted, AcceptedLines: 3, TotalLines: 3,
			ExecutedLines: intPointer(2), PositionFeedback: driveradapter.FeedbackUnavailable,
		},
		driveradapter.Status{
			State: driveradapter.StateRunning, AcceptedLines: 2, TotalLines: 3,
			ExecutedLines: intPointer(1), PositionFeedback: driveradapter.FeedbackUnavailable,
		},
	)

	if _, err := session.Status(context.Background()); err != nil {
		t.Fatalf("initial valid status error = %v", err)
	}
	if _, err := session.Status(context.Background()); !errors.Is(err, driveradapter.ErrProtocol) {
		t.Fatalf("incomplete completed status error = %v", err)
	}
	if _, err := session.Status(context.Background()); err != nil {
		t.Fatalf("valid status after rejected report error = %v", err)
	}
}

func intPointer(value int) *int { return &value }
