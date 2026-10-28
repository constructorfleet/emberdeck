// Package plugin discovers and runs trusted EmberDeck process plugins.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const ProtocolVersion = 1
const manifestFile = "plugin.json"
const maxMessageBytes = 1 << 20
const defaultRunTimeout = 5 * time.Second

// Manifest describes one executable plugin. Executable is relative to the
// directory containing plugin.json; Args are passed directly without a shell.
type Manifest struct {
	ProtocolVersion int      `json:"protocolVersion"`
	Name            string   `json:"name"`
	Executable      string   `json:"executable"`
	Args            []string `json:"args,omitempty"`
}

// Request is the v1 JSON message sent to a plugin on stdin.
type Request struct {
	ProtocolVersion int    `json:"protocolVersion"`
	GCode           string `json:"gcode"`
}

// Response is the v1 JSON message expected on stdout.
type Response struct {
	ProtocolVersion int    `json:"protocolVersion"`
	OK              bool   `json:"ok"`
	Message         string `json:"message,omitempty"`
}

// Plugin is a validated executable and its public manifest.
type Plugin struct {
	Manifest   Manifest
	executable string
}

// Discover reads plugin.json manifests from immediate child directories of
// root, validates their protocol and executable paths, and returns them sorted
// by name. The caller must choose the directory explicitly.
func Discover(root string) ([]Plugin, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read plugin directory: %w", err)
	}
	var out []Plugin
	names := map[string]bool{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		manifestPath := filepath.Join(dir, manifestFile)
		file, err := os.Open(manifestPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", manifestPath, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxMessageBytes+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read %s: %w", manifestPath, readErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close %s: %w", manifestPath, closeErr)
		}
		if len(data) > maxMessageBytes {
			return nil, fmt.Errorf("manifest %s exceeds 1 MiB", manifestPath)
		}
		var m Manifest
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("decode %s: %w", manifestPath, err)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("decode %s: expected one JSON object", manifestPath)
		}
		if m.ProtocolVersion != ProtocolVersion {
			return nil, fmt.Errorf("plugin %q uses unsupported protocol version %d", m.Name, m.ProtocolVersion)
		}
		if m.Name == "" || strings.TrimSpace(m.Name) != m.Name || strings.ContainsAny(m.Name, "/\\\x00") {
			return nil, fmt.Errorf("plugin in %s has invalid name", manifestPath)
		}
		if names[m.Name] {
			return nil, fmt.Errorf("duplicate plugin name %q", m.Name)
		}
		names[m.Name] = true
		if m.Executable == "" || filepath.IsAbs(m.Executable) {
			return nil, fmt.Errorf("plugin %q executable must be a relative path", m.Name)
		}
		base, err := filepath.Abs(dir)
		if err != nil {
			return nil, err
		}
		exe, err := filepath.Abs(filepath.Join(dir, m.Executable))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(base, exe)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("plugin %q executable escapes its directory", m.Name)
		}
		info, err := os.Stat(exe)
		if err != nil {
			return nil, fmt.Errorf("plugin %q executable: %w", m.Name, err)
		}
		if info.IsDir() || (runtime.GOOS != "windows" && info.Mode()&0111 == 0) {
			return nil, fmt.Errorf("plugin %q executable is not executable", m.Name)
		}
		realBase, err := filepath.EvalSymlinks(base)
		if err != nil {
			return nil, fmt.Errorf("resolve plugin directory: %w", err)
		}
		realExe, err := filepath.EvalSymlinks(exe)
		if err != nil {
			return nil, fmt.Errorf("resolve plugin %q executable: %w", m.Name, err)
		}
		realRel, err := filepath.Rel(realBase, realExe)
		if err != nil || realRel == ".." || strings.HasPrefix(realRel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("plugin %q executable escapes its directory through a symlink", m.Name)
		}
		out = append(out, Plugin{Manifest: m, executable: exe})
	}
	sortPlugins(out)
	return out, nil
}

// Run invokes the plugin with one bounded JSON request and reads one bounded
// JSON response. Context cancellation and deadlines terminate the child.
func (p Plugin) Run(ctx context.Context, request Request) (Response, error) {
	if request.ProtocolVersion != ProtocolVersion {
		return Response{}, fmt.Errorf("unsupported request protocol version %d", request.ProtocolVersion)
	}
	input, err := json.Marshal(request)
	if err != nil {
		return Response{}, err
	}
	if len(input) > maxMessageBytes {
		return Response{}, errors.New("plugin request exceeds 1 MiB")
	}
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, defaultRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.executable, p.Manifest.Args...)
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr limitedBuffer
	stdout.limit, stderr.limit = maxMessageBytes, 4096
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		return Response{}, fmt.Errorf("plugin %q failed: %w: %s", p.Manifest.Name, err, strings.TrimSpace(stderr.String()))
	}
	if stdout.overflow {
		return Response{}, errors.New("plugin response exceeds 1 MiB")
	}
	dec := json.NewDecoder(strings.NewReader(stdout.String()))
	dec.DisallowUnknownFields()
	var response Response
	if err := dec.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("decode plugin response: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return Response{}, errors.New("plugin must return exactly one JSON object")
	}
	if response.ProtocolVersion != ProtocolVersion {
		return Response{}, fmt.Errorf("plugin returned unsupported protocol version %d", response.ProtocolVersion)
	}
	return response, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if remaining > n {
			remaining = n
		}
		_, _ = b.Buffer.Write(p[:remaining])
	}
	if remaining < n {
		b.overflow = true
	}
	return n, nil
}

func sortPlugins(plugins []Plugin) {
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].Manifest.Name < plugins[j].Manifest.Name })
}
