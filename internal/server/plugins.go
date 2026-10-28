package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/plugin"
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
	s.mux.HandleFunc("GET /api/v1/plugins", func(w http.ResponseWriter, r *http.Request) {
		manifests := make([]plugin.Manifest, 0, len(plugins))
		for _, p := range plugins {
			manifests = append(manifests, p.Manifest)
		}
		respond(w, 200, manifests)
	})
	return s, nil
}

func (s *Server) preflight(ctx context.Context, id string) error {
	s.mu.Lock()
	job, ok := s.jobs[id]
	s.mu.Unlock()
	if !ok {
		return nil
	} // The lifecycle handler owns not-found responses.
	if _, err := driver.Validate(job.Gcode); err != nil {
		return err
	}
	for _, p := range s.plugins {
		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		result, err := p.Run(timeout, plugin.Request{GCode: job.Gcode})
		cancel()
		if err != nil {
			return fmt.Errorf("preflight %s failed: %w", p.Manifest.Name, err)
		}
		if !result.OK {
			return fmt.Errorf("preflight %s rejected job: %s", p.Manifest.Name, result.Message)
		}
	}
	return nil
}
