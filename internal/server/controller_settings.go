package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
)

// settingsRequestInterval spaces repeated `$$` probe requests to an idle worker
// whose controller settings are unknown, for example after a failed read.
const settingsRequestInterval = 30 * time.Second

// validSettingsReport checks the worker-reported `$$` values and read error.
func validSettingsReport(in *pollReport) bool {
	if len(in.SettingsError) > 1000 {
		return false
	}
	if in.SettingsReadAt != nil && in.SettingsReadAt.After(time.Now().Add(5*time.Second)) {
		return false
	}
	return in.ControllerSettings == nil || in.ControllerSettings.Valid()
}

// recordSettings stores the worker's current settings report. The worker is
// authoritative: a report without settings clears earlier values, so the
// server never compares a job against settings the worker no longer vouches
// for. Callers hold the server mutex.
func recordSettings(d *Driver, in *pollReport) {
	previous := d.ControllerSettings
	d.ControllerSettings, d.SettingsError = in.ControllerSettings, in.SettingsError
	if in.ControllerSettings == nil {
		d.SettingsReadAt = nil
		return
	}
	switch {
	case in.SettingsReadAt != nil:
		readAt := in.SettingsReadAt.UTC()
		d.SettingsReadAt = &readAt
	case previous == nil || d.SettingsReadAt == nil || !sameSettings(*previous, *in.ControllerSettings):
		now := time.Now().UTC()
		d.SettingsReadAt = &now
	}
}

func sameSettings(a, b driver.Settings) bool {
	return len(a.Mismatches(b)) == 0 && len(b.Mismatches(a)) == 0
}

// idleCommand answers an idle poll and asks the worker to read controller
// settings with `$$` while none are known.
func (s *Server) idleCommand(id string) map[string]any {
	response := map[string]any{"command": "idle", "protocol": "1"}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.drivers[id]
	if !ok {
		return response
	}
	if manual := s.manualControls[id]; manual != nil && manual.State == manualStateInterrupted && d.ControllerState != "Idle" {
		if d.statusRequested.IsZero() || time.Since(d.statusRequested) >= time.Second {
			d.statusRequested = time.Now()
			s.drivers[id] = d
			response["read_status"] = true
		}
		return response
	}
	if d.ControllerSettings != nil || d.Busy {
		return response
	}
	if d.settingsRequested.IsZero() || time.Since(d.settingsRequested) >= settingsRequestInterval {
		d.settingsRequested = time.Now()
		s.drivers[id] = d
		response["read_settings"] = true
	}
	return response
}

// expectedSettings returns the controller values a profile-generated job's
// snapshot requires: $30 (max_s), $31 (min_s), and $32 (laser mode). Uploaded
// jobs have no snapshot and return nil.
func expectedSettings(job Job) *driver.Settings {
	if job.Provenance == nil {
		return nil
	}
	profile := job.Provenance.Profile
	maxS, minS, laser := float64(profile.MaxS), float64(profile.MinS), 0.0
	if profile.LaserMode {
		laser = 1
	}
	return &driver.Settings{MaxSpindle: &maxS, MinSpindle: &minS, LaserMode: &laser}
}

// settingsBlock explains why a profile-generated job cannot start on d, or
// returns "" when the driver's reported settings match the job's snapshot.
// Callers hold the server mutex.
func settingsBlock(j jobRecord, d Driver) string {
	expected := expectedSettings(j.Job)
	if expected == nil {
		return ""
	}
	if d.ControllerSettings == nil {
		message := fmt.Sprintf("driver %s has not reported controller settings; wait for its worker to read them with $$ before starting a profile-generated job", d.ID)
		if d.SettingsError != "" {
			message += " (last read failed: " + d.SettingsError + ")"
		}
		return message
	}
	if diff := d.ControllerSettings.Mismatches(*expected); len(diff) > 0 {
		return fmt.Sprintf("controller settings on driver %s differ from the job's profile snapshot: %s. EmberDeck never writes $ settings; correct the controller or generate the job from a matching profile", d.ID, strings.Join(diff, "; "))
	}
	return ""
}

// constantPowerFlag reports whether a program selects GRBL's M3 constant laser
// power mode, in which output stays on at the commanded power while motion
// stops. Comments are ignored; M30 and similar codes are not M3.
func constantPowerFlag(source string) *bool {
	uses := programUsesM3(source)
	return &uses
}

func programUsesM3(source string) bool {
	for _, line := range strings.Split(source, "\n") {
		if cut := strings.IndexByte(line, ';'); cut >= 0 {
			line = line[:cut]
		}
		var compact strings.Builder
		depth := 0
		for _, r := range line {
			switch {
			case r == '(':
				depth++
			case r == ')' && depth > 0:
				depth--
			case depth == 0 && r != ' ' && r != '\t' && r != '\r':
				compact.WriteRune(r)
			}
		}
		text := strings.ToUpper(compact.String())
		for i := 0; i < len(text); i++ {
			if text[i] != 'M' {
				continue
			}
			j := i + 1
			for j < len(text) && (text[j] >= '0' && text[j] <= '9' || text[j] == '.') {
				j++
			}
			if code, err := strconv.ParseFloat(text[i+1:j], 64); err == nil && code == 3 {
				return true
			}
			i = j - 1
		}
	}
	return false
}
