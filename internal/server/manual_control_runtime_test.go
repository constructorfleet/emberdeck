package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/worker"
)

func TestManualHomeRunsUnderSeparateLeaseWithoutJobGCode(t *testing.T) {
	s := openServer(t)
	issued := request(t, s, http.MethodPost, "/api/v1/credentials", `{"name":"manual worker","role":"worker","driver_ids":["bench"]}`, "test-key")
	if issued.Code != http.StatusCreated {
		t.Fatalf("create worker credential: %d %s", issued.Code, issued.Body.String())
	}
	var credential struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &credential); err != nil || credential.Key == "" {
		t.Fatalf("worker credential: %s (%v)", issued.Body.String(), err)
	}
	job := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"queued during manual.gcode","gcode":"M5"}`, "test-key")
	if job.Code != http.StatusCreated {
		t.Fatalf("create queued job: %d %s", job.Code, job.Body.String())
	}
	var queued struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(job.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	manualResponses := 0
	manualResponsesHaveJobData := false
	var connectCount int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/poll") {
			recorder := httptest.NewRecorder()
			s.ServeHTTP(recorder, r)
			var response map[string]any
			_ = json.Unmarshal(recorder.Body.Bytes(), &response)
			command, _ := response["command"].(string)
			if strings.HasPrefix(command, "manual") {
				mu.Lock()
				manualResponses++
				if response["job"] != nil || response["gcode"] != nil {
					manualResponsesHaveJobData = true
				}
				mu.Unlock()
			}
			for key, value := range recorder.Header() {
				for _, v := range value {
					w.Header().Add(key, v)
				}
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		s.ServeHTTP(w, r)
	})
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.RunWithOptions(ctx, endpoint.URL, credential.Key, "bench", func(context.Context) (io.ReadWriteCloser, error) {
			mu.Lock()
			connectCount++
			number := connectCount
			mu.Unlock()
			if number == 1 {
				return driver.NewSimulator(), nil
			} // idle $$ and status probe
			if number > 2 {
				return driver.NewSimulator(), nil
			}
			client, machine := net.Pipe()
			go func() {
				r := bufio.NewReader(machine)
				readByte := func() byte { b, _ := r.ReadByte(); return b }
				readLine := func() { _, _ = r.ReadString('\n') }
				readByte()
				io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
				readLine()
				io.WriteString(machine, "ok\r\n")
				readByte()
				io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
				readLine()
				io.WriteString(machine, "ok\r\n")
				for i := 0; i < 7; i++ {
					if readByte() == 0x85 {
						io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
						return
					}
					io.WriteString(machine, "<Home|MPos:0,0,0>\r\n")
				}
				readByte()
				io.WriteString(machine, "<Idle|MPos:0,0,0>\r\n")
			}()
			return client, nil
		}, worker.Options{Logf: func(string, ...any) {}})
	}()
	t.Cleanup(cancel)

	deadline := time.After(5 * time.Second)
	for {
		response := request(t, s, http.MethodGet, "/api/v1/drivers", "", "test-key")
		var online []struct {
			ID              string   `json:"id"`
			ControllerState string   `json:"controller_state"`
			Capabilities    []string `json:"capabilities"`
		}
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &online) == nil {
			for _, d := range online {
				if d.ID == "bench" && d.ControllerState == "Idle" && containsString(d.Capabilities, "manual_home") {
					goto ready
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("worker did not report Idle/manual capability: %d %s", response.Code, response.Body.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
ready:
	created := request(t, s, http.MethodPost, "/api/v1/drivers/bench/manual", `{"action":"home","confirmed":true}`, "test-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("queue home assignment: %d %s", created.Code, created.Body.String())
	}
	wait := time.After(6 * time.Second)
	for {
		state := request(t, s, http.MethodGet, "/api/v1/drivers/bench/manual", "", "test-key")
		if state.Code == http.StatusOK && strings.Contains(state.Body.String(), `"state":"completed"`) {
			break
		}
		select {
		case <-wait:
			t.Fatalf("manual home did not complete: %d %s", state.Code, state.Body.String())
		case err := <-done:
			t.Fatalf("worker stopped during home: %v", err)
		case <-time.After(25 * time.Millisecond):
		}
	}
	mu.Lock()
	count, leaked := manualResponses, manualResponsesHaveJobData
	mu.Unlock()
	if count < 2 {
		t.Fatalf("manual pickup/progress responses = %d; want assignment and running status", count)
	}
	if leaked {
		t.Fatal("manual assignment response included job G-code")
	}
	state := request(t, s, http.MethodGet, "/api/v1/drivers/bench/manual", "", "test-key")
	if strings.Contains(state.Body.String(), `"lease":"`) {
		t.Fatalf("manual lease secret was exposed: %s", state.Body.String())
	}
	start := request(t, s, http.MethodPost, "/api/v1/jobs/"+queued.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if start.Code != http.StatusOK {
		t.Fatalf("job was not available after manual completion: %d %s", start.Code, start.Body.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
