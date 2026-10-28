package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

const bootstrapKey = "legacy-bootstrap-secret"

type issuedCredential struct {
	ID      string   `json:"id"`
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Role    string   `json:"role"`
	Drivers []string `json:"driver_ids"`
}

func createCredential(t *testing.T, h http.Handler, name, role string, driverIDs []string) issuedCredential {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": name, "role": role, "driver_ids": driverIDs})
	if err != nil {
		t.Fatal(err)
	}
	w := request(t, h, http.MethodPost, "/api/v1/credentials", string(body), bootstrapKey)
	if w.Code != http.StatusCreated {
		t.Fatalf("create %s credential: %d %s", role, w.Code, w.Body.String())
	}
	var result issuedCredential
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.ID == "" || result.Key == "" || result.Role != role {
		t.Fatalf("incomplete credential response: %+v", result)
	}
	return result
}

func TestEveryRegisteredEndpointEnforcesRoleMatrix(t *testing.T) {
	s, err := server.OpenWithPlugins(t.TempDir(), bootstrapKey, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	viewer := createCredential(t, s, "read-only", "viewer", nil)
	operator := createCredential(t, s, "operator", "operator", nil)
	worker := createCredential(t, s, "bench worker", "worker", []string{"bench"})

	type endpoint struct {
		name, method, path, body string
		allowed                  map[string]bool
	}
	roles := func(names ...string) map[string]bool {
		allowed := make(map[string]bool, len(names))
		for _, name := range names {
			allowed[name] = true
		}
		return allowed
	}
	read := roles("viewer", "operator", "admin")
	write := roles("operator", "admin")
	admin := roles("admin")
	workerRoute := roles("worker", "admin") // admin retains the legacy local-worker compatibility path.
	endpoints := []endpoint{
		{"identity", "GET", "/api/v1/auth", "", read},
		{"credential list", "GET", "/api/v1/credentials", "", admin},
		{"credential create", "POST", "/api/v1/credentials", `{"name":"matrix-created","role":"viewer"}`, admin},
		{"credential revoke", "DELETE", "/api/v1/credentials/00000000000000000000000000000000", "", admin},
		{"drivers list", "GET", "/api/v1/drivers", "", read},
		{"worker poll", "POST", "/api/v1/drivers/bench/poll", `{"session":"matrix-session"}`, workerRoute},
		{"job upload", "POST", "/api/v1/jobs", `{"name":"matrix.gcode","gcode":"M5"}`, write},
		{"job list", "GET", "/api/v1/jobs", "", read},
		{"job start", "POST", "/api/v1/jobs/00000000000000000000000000000000/start", `{"driver_id":"bench","confirmed":true}`, write},
		{"job cancel", "POST", "/api/v1/jobs/00000000000000000000000000000000/cancel", `{}`, write},
		{"job pause", "POST", "/api/v1/jobs/00000000000000000000000000000000/pause", `{}`, write},
		{"job resume", "POST", "/api/v1/jobs/00000000000000000000000000000000/resume", `{"confirmed":true}`, write},
		{"job toolpath", "GET", "/api/v1/jobs/00000000000000000000000000000000/toolpath", "", read},
		{"job review", "GET", "/api/v1/jobs/00000000000000000000000000000000/review", "", read},
		{"job get", "GET", "/api/v1/jobs/00000000000000000000000000000000", "", read},
		{"job delete", "DELETE", "/api/v1/jobs/00000000000000000000000000000000", "", write},
		{"plugins list", "GET", "/api/v1/plugins", "", read},
		{"profile list", "GET", "/api/v1/profiles", "", read},
		{"profile create", "POST", "/api/v1/profiles", `{}`, write},
		{"profile get", "GET", "/api/v1/profiles/00000000000000000000000000000000", "", read},
		{"profile update", "PUT", "/api/v1/profiles/00000000000000000000000000000000", `{}`, write},
		{"profile delete", "DELETE", "/api/v1/profiles/00000000000000000000000000000000", `{}`, write},
		{"project list", "GET", "/api/v1/projects", "", read},
		{"project create", "POST", "/api/v1/projects", `{}`, write},
		{"project get", "GET", "/api/v1/projects/00000000000000000000000000000000", "", read},
		{"project update", "PUT", "/api/v1/projects/00000000000000000000000000000000", `{}`, write},
		{"project delete", "DELETE", "/api/v1/projects/00000000000000000000000000000000", `{}`, write},
		{"project generate", "POST", "/api/v1/projects/00000000000000000000000000000000/generate", `{"revision":1}`, write},
		{"svg import", "POST", "/api/v1/svg-import", `<svg xmlns="http://www.w3.org/2000/svg"><line x1="0" y1="0" x2="10" y2="10"/></svg>`, write},
	}
	keys := map[string]string{"viewer": viewer.Key, "operator": operator.Key, "admin": bootstrapKey, "worker": worker.Key}
	for _, endpoint := range endpoints {
		for role, key := range keys {
			t.Run(endpoint.name+"/"+role, func(t *testing.T) {
				w := request(t, s, endpoint.method, endpoint.path, endpoint.body, key)
				if endpoint.allowed[role] {
					if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
						t.Fatalf("authorized %s request rejected with %d: %s", role, w.Code, w.Body.String())
					}
				} else if w.Code != http.StatusForbidden {
					t.Fatalf("unauthorized %s request returned %d, want 403: %s", role, w.Code, w.Body.String())
				}
			})
		}
	}

	// Worker scope is enforced at the poll route, not merely by the role.
	if w := request(t, s, "POST", "/api/v1/drivers/outside-scope/poll", `{"session":"x"}`, worker.Key); w.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope worker poll returned %d: %s", w.Code, w.Body.String())
	}
}

func TestBootstrapCredentialMigrationStoresOnlyHashAndPersistsIdentity(t *testing.T) {
	dir := t.TempDir()
	// This queued job file models an existing single-key deployment before the
	// credential library existed.
	jobID := "0123456789abcdef0123456789abcdef"
	legacyJobs := `{"` + jobID + `":{"id":"` + jobID + `","name":"existing.gcode","gcode":"M5","state":"queued","created":"2026-01-01T00:00:00Z"}}`
	if err := os.WriteFile(filepath.Join(dir, "jobs.json"), []byte(legacyJobs), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	if w := request(t, s, "GET", "/api/v1/jobs/"+jobID, "", bootstrapKey); w.Code != http.StatusOK {
		t.Fatalf("migrated bootstrap admin lost existing jobs: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), bootstrapKey) || strings.Contains(string(stored), `"key"`) {
		t.Fatalf("credential store contains plaintext key material: %s", stored)
	}
	var storedJSON map[string]any
	if err := json.Unmarshal(stored, &storedJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "secret_hash") == false {
		t.Fatalf("credential store does not contain a one-way hash: %s", stored)
	}

	reopened, err := server.Open(dir, "different-environment-key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if w := request(t, reopened, "GET", "/api/v1/auth", "", bootstrapKey); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"admin"`) {
		t.Fatalf("persisted bootstrap credential did not authenticate as admin: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, reopened, "GET", "/api/v1/jobs", "", "different-environment-key"); w.Code != http.StatusUnauthorized {
		t.Fatalf("existing store fell back to changed env key: %d %s", w.Code, w.Body.String())
	}
}

func TestRevokingWorkerDuringActiveLeaseFailsClosedWithoutAffectingAnotherWorker(t *testing.T) {
	s, err := server.Open(t.TempDir(), bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	bench := createCredential(t, s, "bench", "worker", []string{"bench"})
	other := createCredential(t, s, "other", "worker", []string{"other"})
	for _, worker := range []struct{ id, key, session string }{{"bench", bench.Key, "bench-session"}, {"other", other.Key, "other-session"}} {
		if w := request(t, s, "POST", "/api/v1/drivers/"+worker.id+"/poll", `{"session":"`+worker.session+`"}`, worker.key); w.Code != http.StatusOK {
			t.Fatalf("register %s: %d %s", worker.id, w.Code, w.Body.String())
		}
	}
	jobIDs := make(map[string]string)
	for _, driverID := range []string{"bench", "other"} {
		w := request(t, s, "POST", "/api/v1/jobs", `{"name":"`+driverID+`.gcode","gcode":"G1 X1 F100\nM5"}`, bootstrapKey)
		if w.Code != http.StatusCreated {
			t.Fatalf("upload %s job: %d %s", driverID, w.Code, w.Body.String())
		}
		var job struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		jobIDs[driverID] = job.ID
		w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"`+driverID+`","confirmed":true,"bounds_override":true}`, bootstrapKey)
		if w.Code != http.StatusOK {
			t.Fatalf("start %s job: %d %s", driverID, w.Code, w.Body.String())
		}
	}
	leases := make(map[string]string)
	for _, worker := range []struct{ id, key, session string }{{"bench", bench.Key, "bench-session"}, {"other", other.Key, "other-session"}} {
		w := request(t, s, "POST", "/api/v1/drivers/"+worker.id+"/poll", `{"session":"`+worker.session+`"}`, worker.key)
		var assignment struct {
			Lease string `json:"lease"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &assignment); err != nil || w.Code != http.StatusOK || assignment.Lease == "" {
			t.Fatalf("assign %s job: %d %s %v", worker.id, w.Code, w.Body.String(), err)
		}
		leases[worker.id] = assignment.Lease
	}

	w := request(t, s, "DELETE", "/api/v1/credentials/"+bench.ID, "", bootstrapKey)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke active worker: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"bench-session","job_id":"`+jobIDs["bench"]+`","lease":"`+leases["bench"]+`","state":"running","acked":0,"total":2}`, bench.Key)
	if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
		t.Fatalf("revoked worker poll was not rejected: %d %s", w.Code, w.Body.String())
	}
	var revokedJob struct {
		State string `json:"state"`
		Lease string `json:"lease"`
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+jobIDs["bench"], "", bootstrapKey)
	if err := json.Unmarshal(w.Body.Bytes(), &revokedJob); err != nil || w.Code != http.StatusOK {
		t.Fatalf("read interrupted job: %d %s %v", w.Code, w.Body.String(), err)
	}
	if revokedJob.State != "interrupted" {
		t.Fatalf("revoked active job was not interrupted: %+v", revokedJob)
	}
	activeJob := struct {
		State string `json:"state"`
		Lease string `json:"lease"`
	}{}
	w = request(t, s, "GET", "/api/v1/jobs/"+jobIDs["other"], "", bootstrapKey)
	if err := json.Unmarshal(w.Body.Bytes(), &activeJob); err != nil || w.Code != http.StatusOK {
		t.Fatalf("read other active job: %d %s %v", w.Code, w.Body.String(), err)
	}
	if activeJob.State != "running" {
		t.Fatalf("revocation affected another worker: %+v", activeJob)
	}
	w = request(t, s, "POST", "/api/v1/drivers/other/poll", `{"session":"other-session"}`, other.Key)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), leases["other"]) {
		t.Fatalf("other worker could not continue its lease: %d %s", w.Code, w.Body.String())
	}
}

func TestCredentialRotationRequiresNewCredentialBeforeRevokingBootstrap(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	created := createCredential(t, s, "replacement admin", "admin", nil)
	w := request(t, s, "GET", "/api/v1/credentials", "", created.Key)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), created.Key) || strings.Contains(w.Body.String(), "secret_hash") {
		t.Fatalf("credential list leaked secret material or rejected new admin: %d %s", w.Code, w.Body.String())
	}
	var credentials []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Revoked bool   `json:"revoked"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &credentials); err != nil {
		t.Fatal(err)
	}
	bootstrapID := ""
	for _, credential := range credentials {
		if credential.Name == "bootstrap-admin" {
			bootstrapID = credential.ID
		}
	}
	if bootstrapID == "" {
		t.Fatalf("bootstrap credential not listed as revocable: %s", w.Body.String())
	}
	w = request(t, s, "DELETE", "/api/v1/credentials/"+bootstrapID, "", created.Key)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke old bootstrap key after rotation: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, s, "GET", "/api/v1/jobs", "", bootstrapKey); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bootstrap key still works: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, s, "GET", "/api/v1/credentials", "", created.Key); w.Code != http.StatusOK {
		t.Fatalf("replacement admin key stopped working: %d %s", w.Code, w.Body.String())
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, created.Key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if w := request(t, s, "GET", "/api/v1/jobs", "", bootstrapKey); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bootstrap key worked after restart: %d %s", w.Code, w.Body.String())
	}
}

func TestCredentialStorePersistsNamedCredentialAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	created := createCredential(t, s, "reader", "viewer", nil)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), created.Key) || !strings.Contains(string(data), `"secret_hash"`) {
		t.Fatalf("credential store contains plaintext or no hash: %s", data)
	}
	reopened, err := server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	w := request(t, reopened, "GET", "/api/v1/auth", "", created.Key)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"role":"viewer"`) {
		t.Fatalf("named credential did not survive restart: %d %s", w.Code, w.Body.String())
	}
}

func TestLastActiveAdminCannotBeRevoked(t *testing.T) {
	s, err := server.Open(t.TempDir(), bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "GET", "/api/v1/credentials", "", bootstrapKey)
	var credentials []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &credentials); err != nil || w.Code != http.StatusOK {
		t.Fatalf("list bootstrap: %d %s %v", w.Code, w.Body.String(), err)
	}
	if len(credentials) != 1 || credentials[0].Name != "bootstrap-admin" {
		t.Fatalf("unexpected bootstrap credential list: %+v", credentials)
	}
	w = request(t, s, "DELETE", "/api/v1/credentials/"+credentials[0].ID, "", bootstrapKey)
	if w.Code != http.StatusConflict {
		t.Fatalf("revoked final admin: %d %s", w.Code, w.Body.String())
	}
	if w = request(t, s, "GET", "/api/v1/auth", "", bootstrapKey); w.Code != http.StatusOK {
		t.Fatalf("last admin was disabled despite conflict: %d %s", w.Code, w.Body.String())
	}
}

func TestCredentialCreationValidatesRoleScopeAndPreventsOverlappingWorkers(t *testing.T) {
	s, err := server.Open(t.TempDir(), bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	first := createCredential(t, s, "bench", "worker", []string{"bench"})
	if w := request(t, s, "POST", "/api/v1/credentials", `{"name":"duplicate","role":"worker","driver_ids":["bench"]}`, bootstrapKey); w.Code != http.StatusConflict {
		t.Fatalf("duplicate active worker scope returned %d: %s", w.Code, w.Body.String())
	}
	for _, body := range []string{
		`{"name":"missing scope","role":"worker"}`,
		`{"name":"bad scope","role":"worker","driver_ids":["bad/name"]}`,
		`{"name":"bad role","role":"owner"}`,
		`{"name":"operator scope","role":"operator","driver_ids":["bench"]}`,
		`{"name":"duplicate ids","role":"worker","driver_ids":["bench","bench"]}`,
	} {
		if w := request(t, s, "POST", "/api/v1/credentials", body, bootstrapKey); w.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid credential %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	w := request(t, s, "DELETE", "/api/v1/credentials/"+first.ID, "", bootstrapKey)
	if w.Code != http.StatusNoContent {
		t.Fatalf("revoke scoped credential: %d %s", w.Code, w.Body.String())
	}
	if w := request(t, s, "POST", "/api/v1/credentials", `{"name":"replacement","role":"worker","driver_ids":["bench"]}`, bootstrapKey); w.Code != http.StatusCreated {
		t.Fatalf("revoked scope could not be reassigned: %d %s", w.Code, w.Body.String())
	}
}

func TestInvalidCredentialStoreFailsClosedAndFailedCreateDoesNotPublishSecret(t *testing.T) {
	dir := t.TempDir()
	s, err := server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"credentials":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	if opened, err := server.Open(dir, bootstrapKey); err == nil {
		_ = opened.Close()
		t.Fatal("server accepted null credential library")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	s, err = server.Open(dir, bootstrapKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	w := request(t, s, "POST", "/api/v1/credentials", `{"name":"no persist","role":"viewer"}`, bootstrapKey)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), `"key"`) {
		t.Fatalf("failed persistence exposed or published a credential: %d %s", w.Code, w.Body.String())
	}
}
