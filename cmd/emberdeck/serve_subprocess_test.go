package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

const serveHelperEnv = "EMBERDECK_SERVE_HELPER"

// TestServeHelperProcess runs the actual CLI serve path in a child copy of the
// test binary. The environment guard keeps normal test runs from starting it.
func TestServeHelperProcess(t *testing.T) {
	if os.Getenv(serveHelperEnv) != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 == len(os.Args) {
		fmt.Fprintln(os.Stderr, "serve helper: missing command arguments")
		os.Exit(2)
	}
	if err := run(os.Args[separator+1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func TestServeKeepsHTTPUpWhileLocalWorkerRetries(t *testing.T) {
	const adminKey = "serve-e2e-admin-key"
	dataDir := t.TempDir()

	// Persist a worker credential for the local driver. The child first fails to
	// open the deliberately invalid serial port, then revoking this credential
	// gives the worker a deterministic fatal HTTP error to exercise supervision.
	handler, err := server.Open(dataDir, adminKey)
	if err != nil {
		t.Fatal(err)
	}
	createRequest := httptest.NewRequest(http.MethodPost, "/api/v1/credentials", strings.NewReader(`{"name":"local test worker","role":"worker","driver_ids":["local"]}`))
	createRequest.Header.Set("Authorization", "Bearer "+adminKey)
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		_ = handler.Close()
		t.Fatalf("create scoped worker credential: %d %s", createResponse.Code, createResponse.Body.String())
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &created); err != nil || created.Key == "" || created.ID == "" {
		_ = handler.Close()
		t.Fatalf("decode worker credential: id=%q key=%q err=%v", created.ID, created.Key, err)
	}
	if err := handler.Close(); err != nil {
		t.Fatal(err)
	}

	// Reserve a free loopback port, then release it for the child process.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddress := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	const serialPort = "/definitely/not/a/port"
	cmd := exec.Command(os.Args[0], "-test.run=^TestServeHelperProcess$", "--",
		"serve", "-listen", listenAddress, "-data", dataDir, "-serial", serialPort)
	cmd.Env = withEnv(os.Environ(), map[string]string{
		serveHelperEnv:      "1",
		"EMBERDECK_API_KEY": created.Key,
	})
	cmd.Stdout = io.Discard
	logs := &lockedBuffer{}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()
	t.Cleanup(func() {
		select {
		case <-processDone:
			return
		default:
		}
		_ = cmd.Process.Kill()
		select {
		case <-processDone:
		case <-time.After(5 * time.Second):
			t.Error("serve child process did not stop after kill")
		}
	})

	client := &http.Client{Timeout: 750 * time.Millisecond}
	baseURL := "http://" + listenAddress
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	serialFailed := false
	for time.Now().Before(deadline) {
		select {
		case err := <-processDone:
			processDone <- err
			t.Fatalf("serve child exited before worker retry evidence: %v; stderr=%s", err, logs.String())
		default:
		}
		if !ready {
			response, err := client.Get(baseURL + "/")
			if err != nil {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("GET / when serve became ready: status %d", response.StatusCode)
			}
			ready = true
		}

		// Both HTTP endpoints remain live while the serial worker is open and
		// reporting its deterministic controller-open failure.
		drivers := assertHTTPAvailable(t, client, baseURL, adminKey)
		for _, current := range drivers {
			if current.ID == "local" && strings.Contains(current.SettingsError, "serial transport: open") {
				serialFailed = true
				break
			}
		}
		if serialFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !serialFailed {
		t.Fatalf("timed out waiting for the invalid serial connection failure; stderr=%s", logs.String())
	}

	// Revocation removes the local driver immediately; its next poll receives
	// 401, making the worker exit into Supervise's restart loop.
	revokeRequest, err := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/credentials/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	revokeRequest.Header.Set("Authorization", "Bearer "+adminKey)
	revokeResponse, err := client.Do(revokeRequest)
	if err != nil {
		t.Fatalf("revoke local worker credential: %v", err)
	}
	_ = revokeResponse.Body.Close()
	if revokeResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke local worker credential: status %d", revokeResponse.StatusCode)
	}

	for time.Now().Before(deadline) {
		select {
		case err := <-processDone:
			processDone <- err
			t.Fatalf("serve child exited during worker retries: %v; stderr=%s", err, logs.String())
		default:
		}
		assertServeHTTP(t, client, baseURL, adminKey)
		if strings.Count(logs.String(), "stopped: worker: poll: HTTP 401") >= 2 {
			if !strings.Contains(logs.String(), "serial driver "+serialPort) || !strings.Contains(logs.String(), "driver offline, restarting with a new session") {
				t.Fatalf("worker retries were not logged with the real transport: %s", logs.String())
			}
			// Check again after observing retries so the HTTP assertions overlap
			// with a worker that is actively restarting.
			assertServeHTTP(t, client, baseURL, adminKey)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for two local-worker retries; stderr=%s", logs.String())
}

func assertServeHTTP(t *testing.T, client *http.Client, baseURL, adminKey string) {
	t.Helper()
	drivers := assertHTTPAvailable(t, client, baseURL, adminKey)
	if len(drivers) != 0 {
		t.Fatalf("failed local worker unexpectedly remained online: %+v", drivers)
	}
}

type servedDriver struct {
	ID            string `json:"id"`
	SettingsError string `json:"settings_error"`
}

func assertHTTPAvailable(t *testing.T, client *http.Client, baseURL, adminKey string) []servedDriver {
	t.Helper()
	response, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("GET / while worker is failing: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET / while worker is failing: status %d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/drivers", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+adminKey)
	response, err = client.Do(request)
	if err != nil {
		t.Fatalf("GET /api/v1/drivers while worker is failing: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET /api/v1/drivers while worker is failing: status %d: %s", response.StatusCode, body)
	}
	var drivers []servedDriver
	if err := json.NewDecoder(response.Body).Decode(&drivers); err != nil {
		t.Fatalf("decode driver list while worker is failing: %v", err)
	}
	return drivers
}

func withEnv(current []string, updates map[string]string) []string {
	result := make([]string, 0, len(current)+len(updates))
	for _, entry := range current {
		name, _, _ := strings.Cut(entry, "=")
		if _, replace := updates[name]; !replace {
			result = append(result, entry)
		}
	}
	for name, value := range updates {
		result = append(result, name+"="+value)
	}
	return result
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
