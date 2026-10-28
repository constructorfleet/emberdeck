package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

func scrape(t *testing.T, h http.Handler, key string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func metricValue(body, prefix string) (float64, bool) {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				value, err := strconv.ParseFloat(fields[1], 64)
				return value, err == nil
			}
		}
	}
	return 0, false
}

func TestMetricsScrapeContractAndJobLifecycle(t *testing.T) {
	s := openServer(t)
	s.SetBuildVersion("v-test")
	h := s.MetricsHandler("")
	request(t, s, "GET", "/api/v1/jobs", "", "test-key")

	// The initial public scrape contract includes bounded request labels, process
	// collectors, and build information, without requiring an API listener.
	code, body := scrape(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("scrape: %d %s", code, body)
	}
	for _, want := range []string{
		"# HELP emberdeck_build_info", "# TYPE emberdeck_build_info gauge", `emberdeck_build_info{version="v-test"} 1`,
		"# HELP emberdeck_jobs", "# TYPE emberdeck_jobs gauge",
		"# HELP emberdeck_drivers_online", "# HELP emberdeck_drivers_busy", "# HELP emberdeck_oldest_driver_heartbeat_age_seconds",
		"# HELP emberdeck_driver_expirations_total",
		"# HELP go_goroutines", "# HELP process_cpu_seconds_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
	if strings.Contains(body, "emberdeck_plugin_invocations_total") || strings.Contains(body, "emberdeck_plugin_invocation_duration_seconds") {
		t.Fatal("metrics reported preflight plugins when none are configured")
	}

	// Upload adds a durable queued gauge and a new -> queued transition.
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"private-file-name","gcode":"G0 X123.45\nM5"}`, "test-key")
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	var first struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a","capabilities":["pause_resume"]}`, "test-key")
	w = request(t, s, "POST", "/api/v1/jobs/"+first.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	code, body = scrape(t, h, "")
	if code != http.StatusOK || !strings.Contains(body, `emberdeck_jobs{state="running"} 1`) || !strings.Contains(body, "emberdeck_drivers_online 1") || !strings.Contains(body, "emberdeck_drivers_busy 1") {
		t.Fatalf("running assignment gauges incorrect: %d %s", code, body)
	}
	var assignment struct {
		Lease string `json:"lease"`
	}
	// Workers learn the lease from their assignment poll, never from the operator API.
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a","capabilities":["pause_resume"]}`, "test-key")
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	poll := func(state, controller, rawError string) *httptest.ResponseRecorder {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{
			"session": "session-a", "capabilities": []string{"pause_resume"}, "job_id": first.ID,
			"lease": assignment.Lease, "state": state, "controller_state": controller,
			"acked": 1, "total": 2, "error": rawError,
		})
		if state == "completed" {
			payload, _ = json.Marshal(map[string]any{
				"session": "session-a", "capabilities": []string{"pause_resume"}, "job_id": first.ID,
				"lease": assignment.Lease, "state": state, "controller_state": controller,
				"acked": 2, "total": 2, "error": rawError,
			})
		}
		return request(t, s, "POST", "/api/v1/drivers/bench/poll", string(payload), "test-key")
	}
	if w = request(t, s, "POST", "/api/v1/jobs/"+first.ID+"/pause", `{}`, "test-key"); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	if w = poll("paused", "Hold:0", ""); w.Code != http.StatusOK {
		t.Fatalf("confirm pause: %d %s", w.Code, w.Body.String())
	}
	code, body = scrape(t, h, "")
	if code != http.StatusOK || !strings.Contains(body, `emberdeck_jobs{state="paused"} 1`) || !strings.Contains(body, `emberdeck_jobs{state="running"} 0`) || !strings.Contains(body, "emberdeck_drivers_busy 1") {
		t.Fatalf("paused assignment gauges incorrect: %d %s", code, body)
	}
	if w = request(t, s, "POST", "/api/v1/jobs/"+first.ID+"/resume", `{"confirmed":true}`, "test-key"); w.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	if w = poll("running", "Run", ""); w.Code != http.StatusOK {
		t.Fatalf("confirm resume: %d %s", w.Code, w.Body.String())
	}
	code, body = scrape(t, h, "")
	if code != http.StatusOK || !strings.Contains(body, `emberdeck_jobs{state="running"} 1`) || !strings.Contains(body, `emberdeck_jobs{state="paused"} 0`) {
		t.Fatalf("resumed assignment gauges incorrect: %d %s", code, body)
	}
	if w = poll("completed", "Idle", ""); w.Code != http.StatusOK {
		t.Fatalf("complete: %d %s", w.Code, w.Body.String())
	}

	// A second assigned job exercises failure state accounting and confirms that
	// raw worker errors, IDs, API secrets, filenames and G-code are not labels.
	w = request(t, s, "POST", "/api/v1/jobs", `{"name":"another-private-name","gcode":"M5"}`, "test-key")
	var second struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a","capabilities":["pause_resume"]}`, "test-key")
	w = request(t, s, "POST", "/api/v1/jobs/"+second.ID+"/start", `{"driver_id":"bench","confirmed":true,"bounds_override":true}`, "test-key")
	if w.Code != http.StatusOK {
		t.Fatalf("start second: %d %s", w.Code, w.Body.String())
	}
	// Workers learn the lease from their assignment poll, never from the operator API.
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a","capabilities":["pause_resume"]}`, "test-key")
	if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"session": "session-a", "job_id": second.ID, "lease": assignment.Lease, "state": "failed", "acked": 0, "total": 1, "error": "SENSITIVE_RAW_ERROR"})
	if w = request(t, s, "POST", "/api/v1/drivers/bench/poll", string(payload), "test-key"); w.Code != http.StatusOK {
		t.Fatalf("fail: %d %s", w.Code, w.Body.String())
	}

	code, body = scrape(t, h, "")
	if code != http.StatusOK {
		t.Fatalf("scrape after lifecycle: %d %s", code, body)
	}
	for _, want := range []string{
		`emberdeck_jobs{state="queued"} 0`, `emberdeck_jobs{state="running"} 0`,
		`emberdeck_jobs{state="completed"} 1`, `emberdeck_jobs{state="failed"} 1`,
		`emberdeck_job_state_transitions_total{from="new",to="queued"} 2`,
		`emberdeck_job_state_transitions_total{from="queued",to="running"} 2`,
		`emberdeck_job_state_transitions_total{from="running",to="pausing"} 1`,
		`emberdeck_job_state_transitions_total{from="pausing",to="paused"} 1`,
		`emberdeck_job_state_transitions_total{from="paused",to="resuming"} 1`,
		`emberdeck_job_state_transitions_total{from="resuming",to="running"} 1`,
		`emberdeck_job_state_transitions_total{from="running",to="completed"} 1`,
		`emberdeck_job_state_transitions_total{from="running",to="failed"} 1`,
		"# HELP emberdeck_http_requests_total", "# TYPE emberdeck_http_requests_total counter",
		"# HELP emberdeck_http_request_duration_seconds", "# TYPE emberdeck_http_request_duration_seconds histogram",
		"# HELP emberdeck_job_state_transitions_total", "# TYPE emberdeck_job_state_transitions_total counter",
		"emberdeck_http_requests_total", `method="POST"`, `code="201"`,
		`emberdeck_http_request_duration_seconds_count{method="POST"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
	for _, secret := range []string{"test-key", first.ID, second.ID, "private-file-name", "another-private-name", "G0 X123.45", "SENSITIVE_RAW_ERROR"} {
		if strings.Contains(body, secret) {
			t.Errorf("scrape exposed sensitive value %q", secret)
		}
	}
}

func TestMetricsAuthenticationAndDriverExpiration(t *testing.T) {
	s := openServer(t)
	h := s.MetricsHandler("metrics-secret")
	if w := request(t, s, "GET", "/metrics", "", "test-key"); w.Code != http.StatusNotFound {
		t.Fatalf("metrics endpoint was mounted on the API listener: %d", w.Code)
	}
	for _, key := range []string{"", "wrong"} {
		if code, _ := scrape(t, h, key); code != http.StatusUnauthorized {
			t.Errorf("key %q returned status %d", key, code)
		}
	}
	if code, _ := scrape(t, h, "metrics-secret"); code != http.StatusOK {
		t.Fatalf("correct key returned %d", code)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
	code, body := scrape(t, h, "metrics-secret")
	if age, ok := metricValue(body, "emberdeck_oldest_driver_heartbeat_age_seconds "); code != http.StatusOK || !strings.Contains(body, "emberdeck_drivers_online 1") || !ok || age < 0 || age >= 10 {
		t.Fatalf("live driver gauges incorrect: %d age=%v body=%s", code, age, body)
	}
	time.Sleep(10*time.Second + 100*time.Millisecond)
	code, body = scrape(t, h, "metrics-secret")
	if code != http.StatusOK || !strings.Contains(body, `emberdeck_drivers_online 0`) || !strings.Contains(body, `emberdeck_oldest_driver_heartbeat_age_seconds 0`) {
		t.Fatalf("expired driver still online: %d %s", code, body)
	}
	// Expiration is persisted/recorded on the next regular server operation;
	// scrape collection itself is read-only.
	request(t, s, "GET", "/api/v1/drivers", "", "test-key")
	code, body = scrape(t, h, "metrics-secret")
	if code != http.StatusOK || !strings.Contains(body, `emberdeck_driver_expirations_total 1`) {
		t.Fatalf("expiration counter missing: %d %s", code, body)
	}
}

func TestMetricsConcurrentScrapesAndMutations(t *testing.T) {
	s := openServer(t)
	h := s.MetricsHandler("")
	var wg sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				code, body := scrape(t, h, "")
				if code != http.StatusOK || !strings.Contains(body, "emberdeck_jobs") {
					errCh <- fmt.Errorf("scrape returned %d: %s", code, body)
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		body, _ := json.Marshal(map[string]string{"name": fmt.Sprintf("job-%d", i), "gcode": "M5"})
		w := request(t, s, "POST", "/api/v1/jobs", string(body), "test-key")
		if w.Code != http.StatusCreated {
			t.Fatalf("upload during scrape: %d %s", w.Code, w.Body.String())
		}
		w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"session-a"}`, "test-key")
		if w.Code != http.StatusOK {
			t.Fatalf("heartbeat during scrape: %d %s", w.Code, w.Body.String())
		}
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

func TestMetricsDurableJobGaugeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "POST", "/api/v1/jobs", `{"name":"persisted","gcode":"M5"}`, "test-key"); w.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := server.Open(dir, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	code, body := scrape(t, reopened.MetricsHandler(""), "")
	if code != http.StatusOK || !strings.Contains(body, `emberdeck_jobs{state="queued"} 1`) {
		t.Fatalf("durable job gauge was not restored: %d %s", code, body)
	}
}

func TestMetricsMethodAndStatusLabelsAreBounded(t *testing.T) {
	s := openServer(t)
	for _, method := range []string{"PATCH", "TRACE", "nonsense"} {
		r := httptest.NewRequest(method, "/unknown", bytes.NewReader(nil))
		s.ServeHTTP(httptest.NewRecorder(), r)
	}
	_, body := scrape(t, s.MetricsHandler(""), "")
	if strings.Contains(body, `method="PATCH"`) || strings.Contains(body, `method="TRACE"`) || !strings.Contains(body, `method="OTHER"`) {
		t.Fatalf("unbounded method labels: %s", body)
	}
}
