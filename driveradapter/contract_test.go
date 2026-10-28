package driveradapter_test

import (
	"context"
	"errors"
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
