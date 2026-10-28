package plugin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ConstructorFleet/emberdeck/plugin"
)

// This test binary doubles as a real child process, keeping the protocol test
// cross-platform without relying on a shell script.
func TestPluginProcess(t *testing.T) {
	if os.Getenv("EMBERDECK_PLUGIN_HELPER") != "1" {
		return
	}
	switch os.Getenv("EMBERDECK_PLUGIN_MODE") {
	case "sleep":
		time.Sleep(time.Hour)
		os.Exit(0)
	case "overflow":
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", (1<<20)+1)))
		os.Exit(0)
	case "malformed":
		_, _ = os.Stdout.Write([]byte("not-json\n"))
		os.Exit(0)
	case "multiple":
		_, _ = os.Stdout.Write([]byte(`{"protocolVersion":1,"ok":true}{"protocolVersion":1,"ok":true}`))
		os.Exit(0)
	}
	var req plugin.Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		os.Exit(2)
	}
	if req.ProtocolVersion != plugin.ProtocolVersion {
		os.Exit(3)
	}
	resp := plugin.Response{ProtocolVersion: plugin.ProtocolVersion, OK: true, Message: "checked " + req.GCode}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
	os.Exit(0)
}

func writeManifest(t *testing.T, dir, path string, version int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m := plugin.Manifest{ProtocolVersion: version, Name: "fixture", Executable: path}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverAndRunExecutesVersionedPlugin(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(root, "fixture")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	fixtureName := "fixture"
	if runtime.GOOS == "windows" {
		fixtureName += ".exe"
	}
	fixture := filepath.Join(pluginDir, fixtureName)
	contents, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture, contents, 0700); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, pluginDir, fixtureName, plugin.ProtocolVersion)
	t.Setenv("EMBERDECK_PLUGIN_HELPER", "1")
	plugins, err := plugin.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(plugins) != 1 || plugins[0].Manifest.Name != "fixture" {
		t.Fatalf("discovered %#v", plugins)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := plugins[0].Run(ctx, plugin.Request{ProtocolVersion: plugin.ProtocolVersion, GCode: "G28"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Message != "checked G28" {
		t.Fatalf("response %#v", resp)
	}
}

func makeHelper(t *testing.T) plugin.Plugin {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "fixture")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "fixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0700); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, name, plugin.ProtocolVersion)
	ps, err := plugin.Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	return ps[0]
}

func TestRunBoundsBackgroundProcessTime(t *testing.T) {
	p := makeHelper(t)
	t.Setenv("EMBERDECK_PLUGIN_HELPER", "1")
	t.Setenv("EMBERDECK_PLUGIN_MODE", "sleep")
	started := time.Now()
	_, err := p.Run(context.Background(), plugin.Request{ProtocolVersion: plugin.ProtocolVersion, GCode: "G28"})
	if err == nil || time.Since(started) > 8*time.Second {
		t.Fatalf("Run error=%v elapsed=%s", err, time.Since(started))
	}
}

func TestRunRejectsOversizedAndInvalidResponses(t *testing.T) {
	for _, mode := range []string{"overflow", "malformed", "multiple"} {
		t.Run(mode, func(t *testing.T) {
			p := makeHelper(t)
			t.Setenv("EMBERDECK_PLUGIN_HELPER", "1")
			t.Setenv("EMBERDECK_PLUGIN_MODE", mode)
			if _, err := p.Run(context.Background(), plugin.Request{ProtocolVersion: plugin.ProtocolVersion, GCode: "G28"}); err == nil {
				t.Fatal("expected invalid response error")
			}
		})
	}
}

func TestDiscoverRejectsUnsupportedProtocol(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "fixture"), "/bin/true", plugin.ProtocolVersion+1)
	if _, err := plugin.Discover(root); err == nil {
		t.Fatal("expected protocol version error")
	}
}

func TestDiscoverRejectsExecutableOutsidePluginDirectory(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, filepath.Join(root, "fixture"), "../outside", plugin.ProtocolVersion)
	if _, err := plugin.Discover(root); err == nil {
		t.Fatal("expected path containment error")
	}
}

func TestDiscoverRejectsSymlinkOutsidePluginDirectory(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "fixture")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(pluginDir, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeManifest(t, pluginDir, "linked", plugin.ProtocolVersion)
	if _, err := plugin.Discover(root); err == nil {
		t.Fatal("expected symlink containment error")
	}
}
