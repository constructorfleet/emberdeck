package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/internal/server"
	"github.com/ConstructorFleet/emberdeck/worker"
)

func TestGoWorkerRunsCompactProtocolTwoDesignerAssignment(t *testing.T) {
	s := openServer(t)
	profileID := createTestProfile(t, s)
	objects := make([]server.VectorObject, 40)
	for i := range objects {
		x := float64(i + 10)
		objects[i] = server.VectorObject{
			Kind: "path", ID: fmt.Sprintf("path-%d", i), OperationID: "engrave",
			Subpaths: []server.VectorSubpath{{Points: []server.VectorPoint{
				{XMM: x, YMM: 10}, {XMM: x + 1, YMM: 11},
			}}},
		}
	}
	projectBody, err := json.Marshal(struct {
		SchemaVersion   int                      `json:"schema_version"`
		Name            string                   `json:"name"`
		Units           string                   `json:"units"`
		ProfileID       string                   `json:"profile_id"`
		ProfileRevision int                      `json:"profile_revision"`
		Objects         []server.VectorObject    `json:"objects"`
		Operations      []server.VectorOperation `json:"operations"`
	}{2, "many paths", "mm", profileID, 1, objects,
		[]server.VectorOperation{{ID: "engrave", Type: "vector-engrave", FeedMMMin: 600, PowerPercent: 25}}})
	if err != nil {
		t.Fatal(err)
	}
	project := request(t, s, http.MethodPost, "/api/v1/projects", string(projectBody), "test-key")
	var saved struct {
		ID string `json:"id"`
	}
	if project.Code != http.StatusCreated || json.Unmarshal(project.Body.Bytes(), &saved) != nil {
		t.Fatalf("create designer project: %d %s", project.Code, project.Body.String())
	}
	generated := request(t, s, http.MethodPost, "/api/v1/projects/"+saved.ID+"/generate", `{"revision":1}`, "test-key")
	var created struct {
		ID string `json:"id"`
	}
	if generated.Code != http.StatusCreated || json.Unmarshal(generated.Body.Bytes(), &created) != nil {
		t.Fatalf("generate designer job: %d %s", generated.Code, generated.Body.String())
	}
	issued := request(t, s, http.MethodPost, "/api/v1/credentials", `{"name":"designer integration worker","role":"worker","driver_ids":["bench"]}`, "test-key")
	var credential struct {
		Key string `json:"key"`
	}
	if issued.Code != http.StatusCreated || json.Unmarshal(issued.Body.Bytes(), &credential) != nil || credential.Key == "" {
		t.Fatalf("create worker credential: %d %s", issued.Code, issued.Body.String())
	}

	var mu sync.Mutex
	var protocolTwoBody []byte
	var rangeRequests int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		s.ServeHTTP(response, r)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/poll") && response.Code == http.StatusOK {
			var assignment struct {
				Protocol int    `json:"protocol"`
				Command  string `json:"command"`
				Job      struct {
					Content json.RawMessage `json:"content"`
				} `json:"job"`
			}
			if json.Unmarshal(response.Body.Bytes(), &assignment) == nil && assignment.Protocol == 2 && assignment.Command == "run" && len(assignment.Job.Content) != 0 {
				mu.Lock()
				protocolTwoBody = append([]byte(nil), response.Body.Bytes()...)
				mu.Unlock()
			}
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/content") {
			mu.Lock()
			rangeRequests++
			mu.Unlock()
		}
		for name, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = io.Copy(w, response.Body)
	})
	endpoint := httptest.NewServer(handler)
	t.Cleanup(endpoint.Close)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- worker.RunWithOptions(ctx, endpoint.URL, credential.Key, "bench", func(context.Context) (io.ReadWriteCloser, error) {
			return driver.NewSimulator(), nil
		}, worker.Options{Logf: func(string, ...any) {}})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}()

	await(t, func() bool {
		response := request(t, s, http.MethodGet, "/api/v1/drivers", "", "test-key")
		return response.Code == http.StatusOK && strings.Contains(response.Body.String(), `"id":"bench"`)
	})
	started := request(t, s, http.MethodPost, "/api/v1/jobs/"+created.ID+"/start", `{"driver_id":"bench","confirmed":true}`, "test-key")
	if started.Code != http.StatusOK {
		t.Fatalf("dispatch designer job: %d %s", started.Code, started.Body.String())
	}
	await(t, func() bool {
		response := request(t, s, http.MethodGet, "/api/v1/jobs/"+created.ID, "", "test-key")
		var job struct {
			State string `json:"state"`
		}
		return response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &job) == nil && job.State == "completed"
	})

	mu.Lock()
	defer mu.Unlock()
	if len(protocolTwoBody) == 0 {
		t.Fatal("server did not issue a protocol-2 designer assignment")
	}
	if len(protocolTwoBody) > 4096 {
		t.Fatalf("protocol-2 designer assignment is %d bytes, device bound is 4096", len(protocolTwoBody))
	}
	var assignment struct {
		Job struct {
			ID               string          `json:"id"`
			Content          json.RawMessage `json:"content"`
			ExpectedSettings json.RawMessage `json:"expected_settings"`
		} `json:"job"`
	}
	if err := json.Unmarshal(protocolTwoBody, &assignment); err != nil || assignment.Job.ID != created.ID || len(assignment.Job.Content) == 0 {
		t.Fatalf("invalid compact assignment %s (%v)", protocolTwoBody, err)
	}
	if len(assignment.Job.ExpectedSettings) == 0 {
		t.Fatal("compact assignment omitted controller settings")
	}
	if rangeRequests == 0 {
		t.Fatal("Go worker did not fetch chunked job content")
	}
}
