package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/plugin"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

// OpenWithPlugins loads explicitly configured trusted preflight plugins.
// Discovery fails closed; an invalid plugin prevents server startup.
func OpenWithPlugins(dir, key, pluginDir string) (*Server, error) {
	var plugins []plugin.Plugin
	if pluginDir != "" {
		var err error
		plugins, err = plugin.Discover(pluginDir)
		if err != nil {
			return nil, err
		}
	}
	s, err := Open(dir, key)
	if err != nil {
		return nil, err
	}
	s.plugins = plugins
	s.handleAPI("GET /api/v1/plugins", []string{"viewer", "operator", "admin"}, false, func(w http.ResponseWriter, r *http.Request) {
		manifests := make([]plugin.Manifest, 0, len(plugins))
		for _, p := range plugins {
			manifests = append(manifests, p.Manifest)
		}
		respond(w, 200, manifests)
	})
	return s, nil
}

// preflight validates a program and runs trusted plugins outside the server
// mutex. It returns the derived toolpath segment count used for telemetry.
func (s *Server) preflight(ctx context.Context, gcode string) (int, error) {
	if _, err := driver.Validate(gcode); err != nil {
		return 0, err
	}
	for _, p := range s.plugins {
		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		started := time.Now()
		result, err := p.Run(timeout, plugin.Request{ProtocolVersion: plugin.ProtocolVersion, GCode: gcode})
		cancel()
		if err != nil {
			s.observePlugin("error", time.Since(started))
			return 0, fmt.Errorf("preflight %s failed: %w", p.Manifest.Name, err)
		}
		if !result.OK {
			s.observePlugin("rejected", time.Since(started))
			return 0, fmt.Errorf("preflight %s rejected job: %s", p.Manifest.Name, result.Message)
		}
		s.observePlugin("ok", time.Since(started))
	}
	return len(toolpath.Parse(gcode).Segments), nil
}

func (s *Server) observePlugin(outcome string, elapsed time.Duration) {
	s.metrics.pluginInvokes.WithLabelValues(outcome).Inc()
	s.metrics.pluginDuration.WithLabelValues(outcome).Observe(elapsed.Seconds())
}
