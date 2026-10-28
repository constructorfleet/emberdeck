package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driveradapter"
)

type declaredManualAdapter struct{}

func (declaredManualAdapter) Manifest() driveradapter.Manifest {
	return driveradapter.Manifest{
		ProtocolVersion: driveradapter.ProtocolVersion,
		Name:            "declaration-only",
		Version:         "1",
		Transport:       "test",
		Capabilities:    declaredManualCapabilities(),
	}
}

func (declaredManualAdapter) Discover(context.Context) ([]driveradapter.Device, error) {
	return []driveradapter.Device{{ID: "device", Name: "Declaration only", Capabilities: declaredManualCapabilities()}}, nil
}

func (declaredManualAdapter) Configure(context.Context, string, json.RawMessage) (driveradapter.ControllerSession, error) {
	return nil, errors.New("unexpected Configure call")
}

func declaredManualCapabilities() driveradapter.Capabilities {
	return driveradapter.Capabilities{
		ManualHome: true, ManualUnlock: true, ManualJog: true,
		ManualSetZero: true, ManualFrame: true,
	}
}

func TestAdapterManualDeclarationsAreNotAdvertisedAsExecutableControls(t *testing.T) {
	type poll struct {
		Capabilities []string `json:"capabilities"`
	}
	observed := make(chan poll, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request poll
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		observed <- request
		io.WriteString(w, `{"command":"idle"}`)
	}))
	defer server.Close()

	registry := NewAdapterRegistry()
	if err := registry.Register("bench", declaredManualAdapter{}, "device", nil); err != nil {
		t.Fatalf("register declaration-only adapter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWithAdapterRegistry(ctx, server.URL, "key", "bench", registry, Options{RetryMin: time.Millisecond, RetryMax: time.Millisecond})
	}()
	defer cancel()

	select {
	case request := <-observed:
		for _, capability := range []string{"manual_home", "manual_unlock", "manual_jog", "manual_set_zero", "manual_frame"} {
			for _, advertised := range request.Capabilities {
				if advertised == capability {
					t.Fatalf("adapter declaration %q was exposed as an executable worker capability: %#v", capability, request.Capabilities)
				}
			}
		}
	case <-time.After(time.Second):
		t.Fatal("adapter worker did not report capabilities")
	}

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("adapter worker exit = %v; want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("adapter worker did not stop")
	}
}
