package server_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ConstructorFleet/emberdeck/internal/server"
)

func TestServerPluginProcess(t *testing.T) {
	if os.Getenv("EMBERDECK_SERVER_PLUGIN_HELPER") != "1" {
		return
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"protocolVersion": 1, "ok": false, "message": "fixture veto"})
	os.Exit(0)
}

func TestConfiguredPreflightPluginCanVetoDispatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "veto")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := "fixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"protocolVersion": 1, "name": "veto", "executable": name, "args": []string{"-test.run=^TestServerPluginProcess$"}}
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EMBERDECK_SERVER_PLUGIN_HELPER", "1")
	s, err := server.OpenWithPlugins(t.TempDir(), "test-key", root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := request(t, s, "POST", "/api/v1/jobs", `{"name":"test","gcode":"M5"}`, "test-key")
	var job struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	request(t, s, "POST", "/api/v1/drivers/bench/poll", `{"session":"owner"}`, "test-key")
	w = request(t, s, "POST", "/api/v1/jobs/"+job.ID+"/start", `{"driver_id":"bench","confirmed":true}`, "test-key")
	if w.Code != 422 {
		t.Fatalf("plugin veto ignored: %d %s", w.Code, w.Body.String())
	}
	w = request(t, s, "GET", "/api/v1/jobs/"+job.ID, "", "test-key")
	var result struct{ State string }
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.State != "queued" {
		t.Fatalf("vetoed job dispatched: %s", w.Body.String())
	}
}
