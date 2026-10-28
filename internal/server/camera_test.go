package server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

func TestCameraFramesAreAuthenticatedBoundedAndHideUpstreamCredentials(t *testing.T) {
	const username, password = "camera-user", "camera-secret"
	const jpeg = "\xff\xd8simulated-jpeg\xff\xd9"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := r.BasicAuth(); !ok {
			t.Errorf("upstream request was not authenticated")
		}
		gotUser, gotPassword, _ := r.BasicAuth()
		if gotUser != username || gotPassword != password {
			t.Errorf("upstream credentials = %q/%q", gotUser, gotPassword)
		}
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=frame")
		_, _ = fmt.Fprintf(w, "--frame\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n%s\r\n--frame--\r\n", len(jpeg), jpeg)
	}))
	defer upstream.Close()

	dir := t.TempDir()
	s, err := server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	viewer := createCredential(t, s, "camera-viewer", "viewer", nil)
	invalid := `{"url":"http://embedded:secret@127.0.0.1/stream"}`
	if w := request(t, s, http.MethodPut, "/api/v1/drivers/camera-1/camera", invalid, bootstrapKey); w.Code != http.StatusBadRequest {
		t.Fatalf("camera URL with embedded credentials: %d %s", w.Code, w.Body.String())
	}
	config := `{"url":"` + upstream.URL + `","username":"` + username + `","password":"` + password + `"}`
	if w := request(t, s, http.MethodPut, "/api/v1/drivers/camera-1/camera", config, viewer.Key); w.Code != http.StatusForbidden {
		t.Fatalf("viewer configured camera: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, http.MethodPut, "/api/v1/drivers/camera-1/camera", config, bootstrapKey); w.Code != http.StatusNoContent {
		t.Fatalf("admin configured camera: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, http.MethodPost, "/api/v1/drivers/camera-1/poll", `{"session":"camera-session"}`, bootstrapKey); w.Code != http.StatusOK {
		t.Fatalf("register camera driver: %d %s", w.Code, w.Body.String())
	}
	drivers := request(t, s, http.MethodGet, "/api/v1/drivers", "", viewer.Key)
	if drivers.Code != http.StatusOK || !strings.Contains(drivers.Body.String(), `"camera_available":true`) || strings.Contains(drivers.Body.String(), upstream.URL) || strings.Contains(drivers.Body.String(), password) {
		t.Fatalf("driver camera status leaked config or omitted availability: %d %s", drivers.Code, drivers.Body.String())
	}
	if w := request(t, s, http.MethodGet, "/api/v1/drivers/camera-1/camera", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous camera frame: %d", w.Code)
	}
	frame := request(t, s, http.MethodGet, "/api/v1/drivers/camera-1/camera", "", viewer.Key)
	if frame.Code != http.StatusOK || frame.Header().Get("Content-Type") != "image/jpeg" || frame.Body.String() != jpeg {
		t.Fatalf("camera frame: %d %s %q", frame.Code, frame.Header().Get("Content-Type"), frame.Body.String())
	}
	if frame.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("camera frame cache policy = %q", frame.Header().Get("Cache-Control"))
	}
	if strings.Contains(frame.Body.String(), upstream.URL) || strings.Contains(frame.Body.String(), password) {
		t.Fatal("camera response exposed upstream configuration")
	}
	if w := request(t, s, http.MethodGet, "/api/v1/drivers/camera-1/camera", "", viewer.Key); w.Code != http.StatusTooManyRequests {
		t.Fatalf("camera rate limit: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	frame = request(t, restarted, http.MethodGet, "/api/v1/drivers/camera-1/camera", "", viewer.Key)
	if frame.Code != http.StatusOK || frame.Body.String() != jpeg {
		t.Fatalf("camera source did not survive restart: %d %s", frame.Code, frame.Body.String())
	}
}
