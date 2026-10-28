package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/worker"
)

func TestGoWorkerCompletesRealServerChunkLeaseAfterLostReceipt(t *testing.T) {
	s := openServer(t)
	issued := request(t, s, http.MethodPost, "/api/v1/credentials", `{"name":"integrated worker","role":"worker","driver_ids":["bench"]}`, "test-key")
	if issued.Code != http.StatusCreated {
		t.Fatalf("create worker credential: %d %s", issued.Code, issued.Body.String())
	}
	var credential struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &credential); err != nil || credential.Key == "" {
		t.Fatalf("worker credential response: %s (%v)", issued.Body.String(), err)
	}

	const program = "G21\nG90\nM5\n" + "(padding)\n"
	source := program + strings.Repeat("(padding)\n", 8192)
	created := request(t, s, http.MethodPost, "/api/v1/jobs", mustJSON(t, map[string]string{"name": "integrated.gcode", "gcode": source}), "test-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("upload job: %d %s", created.Code, created.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("uploaded job: %s (%v)", created.Body.String(), err)
	}
	if len(source) <= 64*1024 {
		t.Fatal("fixture must require multiple chunks")
	}

	var mu sync.Mutex
	var sessions []string
	var leases []string
	var acknowledged []int
	var chunks []string
	var terminalStates []string
	connects := 0
	lostReceipt := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/poll") {
			var poll struct {
				Session string `json:"session"`
				Lease   string `json:"lease"`
				State   string `json:"state"`
				JobID   string `json:"job_id"`
				Acked   int    `json:"acked"`
			}
			if err := json.Unmarshal(body, &poll); err != nil {
				t.Errorf("decode worker poll: %v", err)
			}
			mu.Lock()
			sessions = append(sessions, poll.Session)
			if poll.Lease != "" {
				leases = append(leases, poll.Lease)
			}
			if poll.JobID != "" {
				acknowledged = append(acknowledged, poll.Acked)
			}
			if poll.State == "completed" || poll.State == "failed" || poll.State == "canceled" {
				terminalStates = append(terminalStates, poll.State)
			}
			lose := poll.State == "completed" && !lostReceipt
			if lose {
				lostReceipt = true
			}
			mu.Unlock()
			if lose {
				recorded := httptest.NewRecorder()
				s.ServeHTTP(recorded, r)
				if recorded.Code != http.StatusOK {
					t.Errorf("first terminal receipt was rejected: %d %s", recorded.Code, recorded.Body.String())
				}
				http.Error(w, "simulated lost terminal acknowledgement", http.StatusServiceUnavailable)
				return
			}
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/content") {
			mu.Lock()
			chunks = append(chunks, r.Header.Get("Range"))
			mu.Unlock()
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
			defer mu.Unlock()
			connects++
			// The worker opens the controller once for its idle settings probe.
			// The next open belongs to the job and must follow complete download.
			if connects > 1 && len(chunks) < 2 {
				return nil, errors.New("controller opened before every content chunk was fetched")
			}
			return driver.NewSimulator(), nil
		}, worker.Options{Logf: func(string, ...any) {}})
	}()

	deadline := time.After(8 * time.Second)
	for {
		response := request(t, s, http.MethodGet, "/api/v1/drivers", "", "test-key")
		var drivers []struct {
			ID       string `json:"id"`
			LastSeen string `json:"last_seen"`
		}
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &drivers) == nil {
			for _, current := range drivers {
				if current.ID == "bench" && current.LastSeen != "" {
					goto online
				}
			}
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("worker did not establish a fresh session: %d %s", response.Code, response.Body.String())
		case <-time.After(20 * time.Millisecond):
		}
	}

online:
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if started.Code != http.StatusOK {
		cancel()
		t.Fatalf("start job: %d %s", started.Code, started.Body.String())
	}
	deadline = time.After(12 * time.Second)
	for {
		response := request(t, s, http.MethodGet, "/api/v1/jobs/"+job.ID, "", "test-key")
		var result struct {
			State string `json:"state"`
		}
		mu.Lock()
		receiptRetried := len(terminalStates) >= 2
		mu.Unlock()
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &result) == nil && result.State == "completed" && receiptRetried {
			break
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("worker stopped before job completion: %v; job response %s", err, response.Body.String())
		case <-deadline:
			cancel()
			t.Fatalf("job did not complete: %d %s", response.Code, response.Body.String())
		case <-time.After(25 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v, want context cancellation", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if connects != 2 {
		t.Fatalf("controller connections = %d, want one settings probe and one job connection", connects)
	}
	if !lostReceipt || len(terminalStates) != 2 || terminalStates[0] != "completed" || terminalStates[1] != "completed" {
		t.Fatalf("terminal receipt attempts = %v (lost acknowledgement injected=%v)", terminalStates, lostReceipt)
	}
	if len(chunks) < 2 || chunks[0] != "bytes=0-65535" || chunks[1] == chunks[0] {
		t.Fatalf("content ranges = %v, want distinct initial chunks", chunks)
	}
	if len(sessions) == 0 || sessions[0] == "" {
		t.Fatal("worker did not create a session")
	}
	for _, session := range sessions {
		if session != sessions[0] {
			t.Fatalf("worker changed session during assignment: %q then %q", sessions[0], session)
		}
	}
	if len(leases) < 2 || leases[0] == "" {
		t.Fatalf("lease receipts = %v", leases)
	}
	for _, lease := range leases {
		if lease != leases[0] {
			t.Fatalf("worker changed lease while reporting: %q then %q", leases[0], lease)
		}
	}
	for i := 1; i < len(acknowledged); i++ {
		if acknowledged[i] < acknowledged[i-1] {
			t.Fatalf("acknowledged progress decreased: %v", acknowledged)
		}
	}
}

func TestGoWorkerRejectsTruncatedRealServerChunkBeforeJobConnection(t *testing.T) {
	s := openServer(t)
	issued := request(t, s, http.MethodPost, "/api/v1/credentials", `{"name":"truncated integration worker","role":"worker","driver_ids":["bench"]}`, "test-key")
	if issued.Code != http.StatusCreated {
		t.Fatalf("create worker credential: %d %s", issued.Code, issued.Body.String())
	}
	var credential struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &credential); err != nil || credential.Key == "" {
		t.Fatalf("worker credential response: %s (%v)", issued.Body.String(), err)
	}
	uploaded := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"truncated.gcode","gcode":"M5\n"}`, "test-key")
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload job: %d %s", uploaded.Code, uploaded.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(uploaded.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("uploaded job: %s (%v)", uploaded.Body.String(), err)
	}

	failedReceipt := make(chan struct{})
	var failOnce sync.Once
	var mu sync.Mutex
	connections := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/content") {
			recorded := httptest.NewRecorder()
			s.ServeHTTP(recorded, r)
			if recorded.Code != http.StatusPartialContent || recorded.Body.Len() < 2 {
				t.Errorf("server content response: %d %q", recorded.Code, recorded.Body.String())
			}
			for key, values := range recorded.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.Header().Del("Content-Length")
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes()[:recorded.Body.Len()-1])
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/poll") {
			var poll struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal(body, &poll); err != nil {
				t.Errorf("decode worker poll: %v", err)
			}
			if poll.State == "failed" {
				recorded := httptest.NewRecorder()
				s.ServeHTTP(recorded, r)
				if recorded.Code != http.StatusOK {
					t.Errorf("failed transfer receipt rejected: %d %s", recorded.Code, recorded.Body.String())
				}
				for key, values := range recorded.Header() {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(recorded.Code)
				_, _ = w.Write(recorded.Body.Bytes())
				failOnce.Do(func() { close(failedReceipt) })
				return
			}
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
			defer mu.Unlock()
			connections++
			return driver.NewSimulator(), nil
		}, worker.Options{Logf: func(string, ...any) {}})
	}()

	deadline := time.Now().Add(8 * time.Second)
	for {
		response := request(t, s, http.MethodGet, "/api/v1/drivers", "", "test-key")
		var drivers []struct {
			ID       string `json:"id"`
			LastSeen string `json:"last_seen"`
		}
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &drivers) == nil {
			for _, current := range drivers {
				if current.ID == "bench" && current.LastSeen != "" {
					goto online
				}
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("worker did not establish a session: %d %s", response.Code, response.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

online:
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if started.Code != http.StatusOK {
		cancel()
		t.Fatalf("start job: %d %s", started.Code, started.Body.String())
	}
	select {
	case <-failedReceipt:
	case <-time.After(8 * time.Second):
		cancel()
		t.Fatal("worker did not report the truncated transfer as failed")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v, want context cancellation", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if connections != 1 {
		t.Fatalf("controller connections = %d, want only the idle settings probe", connections)
	}
	response := request(t, s, http.MethodGet, "/api/v1/jobs/"+job.ID, "", "test-key")
	var result struct {
		State string `json:"state"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || result.State != "failed" {
		t.Fatalf("truncated job state: %d %s", response.Code, response.Body.String())
	}
}

func TestGoWorkerAcceptsLegacyProtocolOneAssignmentFromRealServer(t *testing.T) {
	s := openServer(t)
	issued := request(t, s, http.MethodPost, "/api/v1/credentials", `{"name":"legacy integration worker","role":"worker","driver_ids":["bench"]}`, "test-key")
	if issued.Code != http.StatusCreated {
		t.Fatalf("create worker credential: %d %s", issued.Code, issued.Body.String())
	}
	var credential struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &credential); err != nil || credential.Key == "" {
		t.Fatalf("worker credential response: %s (%v)", issued.Body.String(), err)
	}
	uploaded := request(t, s, http.MethodPost, "/api/v1/jobs", `{"name":"legacy.gcode","gcode":"M5\n"}`, "test-key")
	if uploaded.Code != http.StatusCreated {
		t.Fatalf("upload job: %d %s", uploaded.Code, uploaded.Body.String())
	}
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(uploaded.Body.Bytes(), &job); err != nil || job.ID == "" {
		t.Fatalf("uploaded job: %s (%v)", uploaded.Body.String(), err)
	}

	legacyAssignment := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	contentRequests := 0
	connections := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/poll") {
			var poll map[string]any
			if err := json.Unmarshal(body, &poll); err != nil {
				t.Errorf("decode poll: %v", err)
			}
			// Strip the new capability as an older worker would. This exercises
			// the actual server's protocol 1 response with the current Go client.
			if capabilities, ok := poll["capabilities"].([]any); ok {
				legacyCapabilities := make([]string, 0, len(capabilities))
				for _, capability := range capabilities {
					if capability != "chunked_content_v1" {
						if value, ok := capability.(string); ok {
							legacyCapabilities = append(legacyCapabilities, value)
						}
					}
				}
				poll["capabilities"] = legacyCapabilities
			}
			body, err = json.Marshal(poll)
			if err != nil {
				t.Errorf("encode legacy poll: %v", err)
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			recorded := httptest.NewRecorder()
			s.ServeHTTP(recorded, r)
			var assignment struct {
				Protocol int `json:"protocol"`
				Job      struct {
					GCode string `json:"gcode"`
				} `json:"job"`
			}
			if json.Unmarshal(recorded.Body.Bytes(), &assignment) == nil && assignment.Protocol == 1 && assignment.Job.GCode != "" {
				once.Do(func() { close(legacyAssignment) })
			}
			for key, values := range recorded.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(recorded.Code)
			_, _ = w.Write(recorded.Body.Bytes())
			return
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/content") {
			mu.Lock()
			contentRequests++
			mu.Unlock()
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		s.ServeHTTP(w, r)
	})
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.RunWithOptions(ctx, endpoint.URL, credential.Key, "bench", func(context.Context) (io.ReadWriteCloser, error) {
			mu.Lock()
			connections++
			mu.Unlock()
			return driver.NewSimulator(), nil
		}, worker.Options{Logf: func(string, ...any) {}})
	}()

	deadline := time.Now().Add(8 * time.Second)
	for {
		response := request(t, s, http.MethodGet, "/api/v1/drivers", "", "test-key")
		var drivers []struct {
			ID       string `json:"id"`
			LastSeen string `json:"last_seen"`
		}
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &drivers) == nil {
			for _, current := range drivers {
				if current.ID == "bench" && current.LastSeen != "" {
					goto online
				}
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("worker did not establish a session: %d %s", response.Code, response.Body.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

online:
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if started.Code != http.StatusOK {
		cancel()
		t.Fatalf("start job: %d %s", started.Code, started.Body.String())
	}
	select {
	case <-legacyAssignment:
	case <-time.After(8 * time.Second):
		cancel()
		t.Fatal("server did not return a protocol 1 G-code assignment")
	}
	deadline = time.Now().Add(8 * time.Second)
	for {
		response := request(t, s, http.MethodGet, "/api/v1/jobs/"+job.ID, "", "test-key")
		var result struct {
			State string `json:"state"`
		}
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &result) == nil && result.State == "completed" {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("legacy worker job did not complete: %d %s", response.Code, response.Body.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("worker exit = %v, want context cancellation", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if contentRequests != 0 {
		t.Fatalf("protocol 1 worker fetched chunk content %d times", contentRequests)
	}
	if connections < 2 {
		t.Fatalf("controller connections = %d, want settings probe and legacy job", connections)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
