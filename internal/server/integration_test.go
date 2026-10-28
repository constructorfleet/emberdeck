package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
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
