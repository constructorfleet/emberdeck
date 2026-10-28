package driveradapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ConstructorFleet/emberdeck/driveradapter"
	"github.com/ConstructorFleet/emberdeck/driveradapter/simulator"
)

func TestManualCapabilitiesUseStableNames(t *testing.T) {
	capabilities := driveradapter.Capabilities{
		ManualHome: true, ManualUnlock: true, ManualJog: true,
		ManualSetZero: true, ManualFrame: true,
	}
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"pause_resume":false,"cancel":false,"position_feedback":false,"manual_home":true,"manual_unlock":true,"manual_jog":true,"manual_set_zero":true,"manual_frame":true}`
	if string(encoded) != want {
		t.Fatalf("manual capability JSON = %s, want %s", encoded, want)
	}
}

func TestDiscoverRejectsManualCapabilityUndeclaredByManifest(t *testing.T) {
	for _, test := range []struct {
		name string
		set  func(*driveradapter.Capabilities)
	}{
		{name: "home", set: func(c *driveradapter.Capabilities) { c.ManualHome = true }},
		{name: "unlock", set: func(c *driveradapter.Capabilities) { c.ManualUnlock = true }},
		{name: "jog", set: func(c *driveradapter.Capabilities) { c.ManualJog = true }},
		{name: "set zero", set: func(c *driveradapter.Capabilities) { c.ManualSetZero = true }},
		{name: "frame", set: func(c *driveradapter.Capabilities) { c.ManualFrame = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			deviceCapabilities := driveradapter.Capabilities{}
			test.set(&deviceCapabilities)
			adapter := manualCapabilityAdapter{
				Adapter: simulator.New(), deviceCapabilities: deviceCapabilities,
			}
			if _, err := driveradapter.Discover(context.Background(), adapter); !errors.Is(err, driveradapter.ErrProtocol) {
				t.Fatalf("undeclared manual capability error = %v", err)
			}
		})
	}
}

func TestDiscoverPreservesDeclaredManualCapabilities(t *testing.T) {
	capabilities := driveradapter.Capabilities{
		ManualHome: true, ManualUnlock: true, ManualJog: true,
		ManualSetZero: true, ManualFrame: true,
	}
	adapter := manualCapabilityAdapter{
		Adapter: simulator.New(), manifestCapabilities: capabilities,
		deviceCapabilities: capabilities,
	}
	devices, err := driveradapter.Discover(context.Background(), adapter)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].Capabilities != capabilities {
		t.Fatalf("discovered capabilities = %#v, want %#v", devices, capabilities)
	}
}

type manualCapabilityAdapter struct {
	*simulator.Adapter
	manifestCapabilities driveradapter.Capabilities
	deviceCapabilities   driveradapter.Capabilities
}

func (a manualCapabilityAdapter) Manifest() driveradapter.Manifest {
	manifest := a.Adapter.Manifest()
	manifest.Capabilities = a.manifestCapabilities
	return manifest
}

func (a manualCapabilityAdapter) Discover(ctx context.Context) ([]driveradapter.Device, error) {
	devices, err := a.Adapter.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for i := range devices {
		devices[i].Capabilities = a.deviceCapabilities
	}
	return devices, nil
}
