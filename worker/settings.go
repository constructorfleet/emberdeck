package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/jobtransfer"
)

// settingsProbeTimeout bounds an idle `$$` probe, including opening the
// controller connection.
const settingsProbeTimeout = 10 * time.Second

// controllerReport holds the most recent controller settings this worker read
// with `$$`, and attaches them to every poll so the server can compare them
// with a job's profile before dispatch.
type controllerReport struct {
	mu       sync.Mutex
	settings *driver.Settings
	readAt   time.Time
	err      string
	// reset holds a probe's reset-banner error until it refuses the next job:
	// the probe consumed the banner a later connection would have shown.
	reset string
}

func (r *controllerReport) poll(session string, p *progress) pollRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	request := pollRequest{Session: session, Capabilities: []string{"pause_resume", jobtransfer.Capability}, progress: p, SettingsError: r.err}
	if r.settings != nil {
		settings, readAt := *r.settings, r.readAt
		request.ControllerSettings, request.SettingsReadAt = &settings, &readAt
	}
	return request
}

func (r *controllerReport) record(settings driver.Settings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.settings, r.readAt, r.err = &settings, time.Now().UTC(), ""
}

func (r *controllerReport) failed(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Earlier values may be stale; the server must not compare against them.
	r.settings, r.err = nil, truncate(err.Error(), 500)
	if strings.Contains(err.Error(), "controller reset") {
		r.reset = err.Error()
	}
}

func (r *controllerReport) takeReset() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	reset := r.reset
	r.reset = ""
	return reset
}

// verify records settings read during job readiness and refuses the job when
// they differ from its profile snapshot. It runs before any program byte.
func (r *controllerReport) verify(settings driver.Settings, expected *driver.Settings) error {
	r.record(settings)
	if expected == nil {
		return nil
	}
	if diff := settings.Mismatches(*expected); len(diff) > 0 {
		return fmt.Errorf("controller settings differ from the job's profile snapshot: %s", strings.Join(diff, "; "))
	}
	return nil
}

// probe opens the controller only to read its settings with `$$`, then closes
// it. It never writes settings or program bytes.
func (r *controllerReport) probe(ctx context.Context, connect Connector) {
	probeCtx, cancel := context.WithTimeout(ctx, settingsProbeTimeout)
	defer cancel()
	conn, err := connect(probeCtx)
	if err == nil && conn == nil {
		err = errors.New("connector returned nil connection")
	}
	if err != nil {
		r.failed(fmt.Errorf("worker: open controller for settings read: %w", err))
		return
	}
	defer conn.Close()
	settings, err := driver.ReadSettings(probeCtx, conn, settingsProbeTimeout)
	if err != nil {
		r.failed(err)
		return
	}
	r.record(settings)
}

func truncate(value string, limit int) string {
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
