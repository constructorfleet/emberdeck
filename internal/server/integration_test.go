package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/driveradapter"
	"github.com/ConstructorFleet/emberdeck/driveradapter/simulator"
	"github.com/ConstructorFleet/emberdeck/worker"
)

// This crosses real HTTP, worker dispatch, and the controller wire simulator.
func TestDistributedWorkerExecutesAndCancelsJobs(t *testing.T) {
	s := openServer(t)
	remote := httptest.NewServer(s)
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- worker.Run(ctx, remote.URL, "test-key", "simulator", func(context.Context) (io.ReadWriteCloser, error) { return driver.NewSimulator(), nil })
	}()
	defer func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}()
	await(t, func() bool {
		w := request(t, s, "GET", "/api/v1/drivers", "", "test-key")
		return strings.Contains(w.Body.String(), `"id":"simulator"`)
	})
	upload := func(program string) string {
		t.Helper()
		data, _ := json.Marshal(map[string]string{"name": "integration.gcode", "gcode": program})
		w := request(t, s, "POST", "/api/v1/jobs", string(data), "test-key")
		var job struct{ ID string }
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 201 {
			t.Fatalf("upload: %d %s", w.Code, w.Body.String())
		}
		w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"simulator","confirmed":true,"bounds_override":true}`, "test-key")
		if w.Code != 200 {
			t.Fatalf("dispatch: %d %s", w.Code, w.Body.String())
		}
		return job.ID
	}
	state := func(id string) (string, int) {
		w := request(t, s, "GET", "/api/v1/jobs/"+id, "", "test-key")
		var job struct {
			State string
			Acked int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		return job.State, job.Acked
	}
	id := upload("G21\nG90\nG1 X10 F100\nM5")
	await(t, func() bool { state, acked := state(id); return state == "completed" && acked == 4 })
	id = upload(strings.Repeat("M5\n", 1500))
	await(t, func() bool { state, acked := state(id); return state == "running" && acked > 0 })
	w := request(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", `{}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
	}
	await(t, func() bool { state, _ := state(id); return state == "canceled" })
}

func TestRegisteredAdapterExecutesHTTPAssignedJob(t *testing.T) {
	s := openServer(t)
	remote := httptest.NewServer(s)
	defer remote.Close()
	adapter := simulator.New()
	registry := worker.NewAdapterRegistry()
	if err := registry.Register("adapter-simulator", adapter, "simulator-1", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- worker.RunWithAdapterRegistry(ctx, remote.URL, "test-key", "adapter-simulator", registry, worker.Options{})
	}()
	defer func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("adapter worker did not stop")
		}
	}()
	await(t, func() bool {
		w := request(t, s, "GET", "/api/v1/drivers", "", "test-key")
		return strings.Contains(w.Body.String(), `"id":"adapter-simulator"`)
	})
	data, _ := json.Marshal(map[string]string{"name": "adapter.gcode", "gcode": "G21\nG90\nG1 X10 F100\nM5"})
	w := request(t, s, "POST", "/api/v1/jobs", string(data), "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"adapter-simulator","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("dispatch: %d %s", w.Code, w.Body.String())
	}
	await(t, func() bool {
		w := request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
		var receipt struct {
			State string
			Acked int
			Total int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		return receipt.State == "completed" && receipt.Acked == 4 && receipt.Total == 4
	})
	if got := adapter.StartCount(); got != 1 {
		t.Fatalf("adapter Start count = %d, want exactly one", got)
	}
}

func TestAdapterConnectingAndCancellingStatusesContinueLeasePolling(t *testing.T) {
	s := openServer(t)
	remote := httptest.NewServer(s)
	defer remote.Close()
	adapter := &sequencedStatusAdapter{inner: simulator.New(), states: []driveradapter.State{driveradapter.StateConnecting, driveradapter.StateCancelling, driveradapter.StateCompleted}}
	registry := worker.NewAdapterRegistry()
	if err := registry.Register("adapter-status-sequence", adapter, "simulator-1", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workerDone := make(chan error, 1)
	go func() {
		workerDone <- worker.RunWithAdapterRegistry(ctx, remote.URL, "test-key", "adapter-status-sequence", registry, worker.Options{})
	}()
	defer func() {
		cancel()
		select {
		case <-workerDone:
		case <-time.After(5 * time.Second):
			t.Error("adapter worker did not stop")
		}
	}()
	await(t, func() bool {
		w := request(t, s, "GET", "/api/v1/drivers", "", "test-key")
		return strings.Contains(w.Body.String(), `"id":"adapter-status-sequence"`)
	})
	data, _ := json.Marshal(map[string]string{"name": "adapter-status.gcode", "gcode": "G21\nG90\nG1 X10 F100\nM5"})
	w := request(t, s, "POST", "/api/v1/jobs", string(data), "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 201 {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"adapter-status-sequence","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("dispatch: %d %s", w.Code, w.Body.String())
	}
	await(t, func() bool {
		w := request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
		var receipt struct {
			State string
			Acked int
			Total int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		return receipt.State == "completed" && receipt.Acked == 4 && receipt.Total == 4
	})
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.statusCalls != len(adapter.states) {
		t.Fatalf("adapter Status calls = %d, want %d", adapter.statusCalls, len(adapter.states))
	}
}

func TestRegisteredAdapterFailuresFailLeaseWithoutReplay(t *testing.T) {
	for _, failurePoint := range []string{"start", "status"} {
		t.Run(failurePoint, func(t *testing.T) {
			s := openServer(t)
			remote := httptest.NewServer(s)
			defer remote.Close()
			adapter := &failingAdapter{inner: simulator.New(), failurePoint: failurePoint}
			registry := worker.NewAdapterRegistry()
			if err := registry.Register("adapter-failure", adapter, "simulator-1", nil); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			workerDone := make(chan error, 1)
			go func() {
				workerDone <- worker.RunWithAdapterRegistry(ctx, remote.URL, "test-key", "adapter-failure", registry, worker.Options{})
			}()
			defer func() {
				cancel()
				select {
				case <-workerDone:
				case <-time.After(5 * time.Second):
					t.Error("adapter worker did not stop")
				}
			}()
			await(t, func() bool {
				w := request(t, s, "GET", "/api/v1/drivers", "", "test-key")
				return strings.Contains(w.Body.String(), `"id":"adapter-failure"`)
			})
			data, _ := json.Marshal(map[string]string{"name": "adapter-failure.gcode", "gcode": "G21\nG90\nG1 X10 F100\nM5"})
			w := request(t, s, "POST", "/api/v1/jobs", string(data), "test-key")
			var job struct{ ID string }
			if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 201 {
				t.Fatalf("upload: %d %s", w.Code, w.Body.String())
			}
			w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"adapter-failure","confirmed":true,"bounds_override":true}`, "test-key")
			if w.Code != 200 {
				t.Fatalf("dispatch: %d %s", w.Code, w.Body.String())
			}
			await(t, func() bool {
				w := request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
				var receipt struct{ State string }
				if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
					t.Fatal(err)
				}
				return receipt.State == "failed"
			})
			adapter.mu.Lock()
			starts, lease := adapter.starts, adapter.lease
			adapter.mu.Unlock()
			if starts != 1 || lease == "" {
				t.Fatalf("adapter Start attempts = %d, lease = %q; want one attempt under a lease", starts, lease)
			}
			time.Sleep(1100 * time.Millisecond)
			adapter.mu.Lock()
			startsAfterIdle := adapter.starts
			adapter.mu.Unlock()
			if startsAfterIdle != 1 {
				t.Fatalf("adapter Start replayed after failure: %d attempts", startsAfterIdle)
			}
		})
	}
}

type failingAdapter struct {
	inner        *simulator.Adapter
	failurePoint string
	mu           sync.Mutex
	starts       int
	lease        string
}

func (a *failingAdapter) Manifest() driveradapter.Manifest { return a.inner.Manifest() }
func (a *failingAdapter) Discover(ctx context.Context) ([]driveradapter.Device, error) {
	return a.inner.Discover(ctx)
}
func (a *failingAdapter) Configure(ctx context.Context, deviceID string, config json.RawMessage) (driveradapter.ControllerSession, error) {
	session, err := a.inner.Configure(ctx, deviceID, config)
	if err != nil {
		return nil, err
	}
	return &failingSession{ControllerSession: session, adapter: a}, nil
}

type failingSession struct {
	driveradapter.ControllerSession
	adapter *failingAdapter
}

type sequencedStatusAdapter struct {
	inner       *simulator.Adapter
	states      []driveradapter.State
	mu          sync.Mutex
	statusCalls int
}

func (a *sequencedStatusAdapter) Manifest() driveradapter.Manifest { return a.inner.Manifest() }
func (a *sequencedStatusAdapter) Discover(ctx context.Context) ([]driveradapter.Device, error) {
	return a.inner.Discover(ctx)
}
func (a *sequencedStatusAdapter) Configure(ctx context.Context, deviceID string, config json.RawMessage) (driveradapter.ControllerSession, error) {
	session, err := a.inner.Configure(ctx, deviceID, config)
	if err != nil {
		return nil, err
	}
	return &sequencedStatusSession{ControllerSession: session, adapter: a}, nil
}

type sequencedStatusSession struct {
	driveradapter.ControllerSession
	adapter *sequencedStatusAdapter
}

func (s *sequencedStatusSession) Status(ctx context.Context) (driveradapter.Status, error) {
	status, err := s.ControllerSession.Status(ctx)
	if err != nil {
		return status, err
	}
	s.adapter.mu.Lock()
	defer s.adapter.mu.Unlock()
	if s.adapter.statusCalls < len(s.adapter.states) {
		status.State = s.adapter.states[s.adapter.statusCalls]
		s.adapter.statusCalls++
		if status.State != driveradapter.StateCompleted {
			status.ControllerState = "Run"
		}
	}
	return status, nil
}

func (s *failingSession) Start(ctx context.Context, assignment driveradapter.Assignment) error {
	s.adapter.mu.Lock()
	s.adapter.starts++
	s.adapter.lease = assignment.LeaseID
	fail := s.adapter.failurePoint == "start"
	s.adapter.mu.Unlock()
	if fail {
		return errors.New("injected Start failure")
	}
	return s.ControllerSession.Start(ctx, assignment)
}
func (s *failingSession) Status(ctx context.Context) (driveradapter.Status, error) {
	if s.adapter.failurePoint == "status" {
		return driveradapter.Status{}, errors.New("injected Status failure")
	}
	return s.ControllerSession.Status(ctx)
}

func await(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for distributed job lifecycle")
}
