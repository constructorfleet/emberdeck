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

func TestDistributedWorkerPausesResumesAndCancelsHeldJob(t *testing.T) {
	s := openServer(t)
	remote := httptest.NewServer(s)
	defer remote.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.Run(ctx, remote.URL, "test-key", "simulator", func(context.Context) (io.ReadWriteCloser, error) { return driver.NewSimulator(), nil })
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Logf("worker stopped: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("held worker did not stop")
		}
	}()
	await(t, func() bool {
		w := request(t, s, "GET", "/api/v1/drivers", "", "test-key")
		return strings.Contains(w.Body.String(), `"pause_resume"`)
	})
	start := func() string {
		t.Helper()
		payload, _ := json.Marshal(map[string]string{"name": "pause.gcode", "gcode": strings.Repeat("G1 X1 F100\n", 1200)})
		w := request(t, s, "POST", "/api/v1/jobs", string(payload), "test-key")
		var job struct{ ID string }
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 201 {
			t.Fatalf("upload: %d %s", w.Code, w.Body.String())
		}
		w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"simulator","confirmed":true,"bounds_override":true}`, "test-key")
		if w.Code != 200 {
			t.Fatalf("start: %d %s", w.Code, w.Body.String())
		}
		return job.ID
	}
	type jobState struct {
		State           string
		Acked           int
		ControllerState string `json:"controller_state"`
	}
	read := func(id string) jobState {
		t.Helper()
		w := request(t, s, "GET", "/api/v1/jobs/"+id, "", "test-key")
		var job jobState
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil || w.Code != 200 {
			t.Fatalf("job: %d %s", w.Code, w.Body.String())
		}
		return job
	}
	pause := func(id string) {
		t.Helper()
		await(t, func() bool { return read(id).Acked > 0 })
		w := request(t, s, "POST", "/api/v1/jobs/"+id+"/pause", `{}`, "test-key")
		if w.Code != 200 {
			t.Fatalf("pause: %d %s", w.Code, w.Body.String())
		}
		await(t, func() bool { return read(id).State == "paused" })
		held := read(id)
		if held.ControllerState != "Hold:0" && held.ControllerState != "Idle" {
			t.Fatalf("pause not confirmed: %+v", held)
		}
	}
	id := start()
	pause(id)
	held := read(id)
	seen := func() time.Time {
		t.Helper()
		w := request(t, s, "GET", "/api/v1/drivers", "", "test-key")
		var drivers []struct {
			LastSeen time.Time `json:"last_seen"`
			Busy     bool
		}
		if err := json.Unmarshal(w.Body.Bytes(), &drivers); err != nil || len(drivers) != 1 || !drivers[0].Busy {
			t.Fatalf("held ownership: %s", w.Body.String())
		}
		return drivers[0].LastSeen
	}
	// Observe two distinct live heartbeat receipts rather than an arbitrary sleep.
	for i := 0; i < 2; i++ {
		before := seen()
		await(t, func() bool { return seen().After(before) })
	}
	if after := read(id); after.State != "paused" || after.Acked != held.Acked {
		t.Fatalf("held source advanced: before=%+v after=%+v", held, after)
	}
	w := request(t, s, "POST", "/api/v1/jobs/"+id+"/resume", `{"confirmed":true}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	await(t, func() bool { return read(id).State == "completed" })
	if final := read(id); final.Acked != 1200 || final.ControllerState != "Idle" {
		t.Fatalf("completion: %+v", final)
	}
	id = start()
	pause(id)
	w = request(t, s, "POST", "/api/v1/jobs/"+id+"/cancel", `{}`, "test-key")
	if w.Code != 200 {
		t.Fatalf("held cancel: %d %s", w.Code, w.Body.String())
	}
	await(t, func() bool { return read(id).State == "canceled" })
}
