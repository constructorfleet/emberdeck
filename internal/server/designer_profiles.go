package server

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MachineProfile contains operator-declared machine values. EmberDeck neither
// measures nor configures these values on the controller.
type MachineProfile struct {
	SchemaVersion int     `json:"schema_version"`
	ID            string  `json:"id"`
	Revision      int     `json:"revision"`
	Name          string  `json:"name"`
	Controller    string  `json:"controller"`
	Units         string  `json:"units"`
	Origin        string  `json:"origin"`
	WidthMM       float64 `json:"width_mm"`
	HeightMM      float64 `json:"height_mm"`
	MinS          int     `json:"min_s"`
	MaxS          int64   `json:"max_s"`
	MinFeed       float64 `json:"min_feed_mm_min"`
	MaxFeed       float64 `json:"max_feed_mm_min"`
	MaxPowerPct   float64 `json:"max_power_percent"`
	LaserMode     bool    `json:"laser_mode_enabled"`
}

type machineProfileJSON MachineProfile

func (profile *MachineProfile) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if !explicitZero(fields["min_s"]) {
		return errors.New("machine profile must explicitly declare min_s as zero")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded machineProfileJSON
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*profile = MachineProfile(decoded)
	return nil
}

type profileRequest struct {
	SchemaVersion    int             `json:"schema_version"`
	ID               json.RawMessage `json:"id"`
	Revision         json.RawMessage `json:"revision"`
	Name             string          `json:"name"`
	Controller       string          `json:"controller"`
	Units            string          `json:"units"`
	Origin           string          `json:"origin"`
	WidthMM          float64         `json:"width_mm"`
	HeightMM         float64         `json:"height_mm"`
	MaxS             int64           `json:"max_s"`
	MinFeed          float64         `json:"min_feed_mm_min"`
	MaxFeed          float64         `json:"max_feed_mm_min"`
	MaxPowerPct      float64         `json:"max_power_percent"`
	LaserMode        bool            `json:"laser_mode_enabled"`
	DeclaredMinS     json.RawMessage `json:"min_s"`
	ExpectedRevision int             `json:"-"`
}

func (request profileRequest) profile() MachineProfile {
	return MachineProfile{SchemaVersion: request.SchemaVersion, Name: request.Name, Controller: request.Controller, Units: request.Units, Origin: request.Origin,
		WidthMM: request.WidthMM, HeightMM: request.HeightMM, MinS: 0, MaxS: request.MaxS, MinFeed: request.MinFeed, MaxFeed: request.MaxFeed,
		MaxPowerPct: request.MaxPowerPct, LaserMode: request.LaserMode}
}

// loadOptionalJSON treats an absent library as empty but rejects corrupt or null data.
func loadOptionalJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("invalid trailing data in designer library")
	}
	return nil
}

// durableJSON atomically replaces a library and syncs its containing directory.
// The bool reports that rename happened, so callers can fail closed if the
// final directory sync is uncertain.
func durableJSON(path string, value any) (bool, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".designer-*.tmp")
	if err != nil {
		return false, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return false, err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return false, err
	}
	if err = syncDirectory(dir); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Server) saveProfiles(profiles map[string]MachineProfile) error {
	if s.closed {
		return errors.New("server is closed")
	}
	if s.storageErr != nil {
		return s.storageErr
	}
	published, err := durableJSON(filepath.Join(s.dir, "profiles.json"), profiles)
	if published && err != nil {
		s.storageErr = err
	}
	if err != nil {
		return err
	}
	s.profiles = profiles
	return nil
}

func decodeProfile(w http.ResponseWriter, r *http.Request, request *profileRequest, updating bool) bool {
	if !decodeBounded(w, r, request, 64<<10) {
		return false
	}
	request.Name = strings.TrimSpace(request.Name)
	if len(request.ID) != 0 || !parseRevisionField(request.Revision, updating, &request.ExpectedRevision) || !explicitZero(request.DeclaredMinS) || !validProfileValues(request.profile()) {
		fail(w, 400, "invalid GRBL machine profile")
		return false
	}
	return true
}

func parseRevisionField(raw json.RawMessage, updating bool, out *int) bool {
	if !updating {
		return len(raw) == 0
	}
	if len(raw) == 0 || json.Unmarshal(raw, out) != nil || *out < 1 {
		return false
	}
	return true
}

func explicitZero(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value int
	return json.Unmarshal(raw, &value) == nil && value == 0
}

func validProfileValues(profile MachineProfile) bool {
	return profile.SchemaVersion == 1 && profile.Name == strings.TrimSpace(profile.Name) && profile.Name != "" && len(profile.Name) <= 120 &&
		profile.Controller == "grbl-1.1" && profile.Units == "mm" && profile.Origin == "lower-left" && profile.LaserMode && profile.MinS == 0 &&
		finite(profile.WidthMM) && finite(profile.HeightMM) && finite(profile.MinFeed) && finite(profile.MaxFeed) && finite(profile.MaxPowerPct) &&
		validPrecision(profile.WidthMM) && validPrecision(profile.HeightMM) && validPrecision(profile.MinFeed) && validPrecision(profile.MaxFeed) && validPrecision(profile.MaxPowerPct) &&
		profile.WidthMM > 0 && profile.WidthMM <= 10000 && profile.HeightMM > 0 && profile.HeightMM <= 10000 && profile.MaxS >= 1 && profile.MaxS <= 1_000_000_000 &&
		profile.MinFeed > 0 && profile.MaxFeed >= profile.MinFeed && profile.MaxFeed <= 100000 && profile.MaxPowerPct > 0 && profile.MaxPowerPct <= 100
}

func decodeBounded(w http.ResponseWriter, r *http.Request, value any, max int64) bool {
	body := http.MaxBytesReader(w, r.Body, max)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		fail(w, 400, "invalid request JSON")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		fail(w, 400, "expected one JSON object")
		return false
	}
	return true
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func finite(v float64) bool         { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func validPrecision(v float64) bool { return math.Abs(v*1000-math.Round(v*1000)) < 1e-8 }

func (s *Server) profileList() []MachineProfile {
	ids := make([]string, 0, len(s.profiles))
	for id := range s.profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]MachineProfile, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.profiles[id])
	}
	return out
}

func (s *Server) listProfiles(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	respond(w, http.StatusOK, s.profileList())
}

func (s *Server) createProfile(w http.ResponseWriter, r *http.Request) {
	var input profileRequest
	if !decodeProfile(w, r, &input, false) {
		return
	}
	id, err := randomID()
	if err != nil {
		fail(w, 500, "could not allocate profile ID")
		return
	}
	profile := input.profile()
	profile.ID, profile.Revision = id, 1
	s.mu.Lock()
	defer s.mu.Unlock()
	profiles := copyProfiles(s.profiles)
	profiles[id] = profile
	if err := s.saveProfiles(profiles); err != nil {
		fail(w, 500, "could not persist profile")
		return
	}
	w.Header().Set("Location", "/api/v1/profiles/"+id)
	respond(w, http.StatusCreated, profile)
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile, ok := s.profiles[r.PathValue("id")]
	if !ok {
		fail(w, 404, "profile not found")
		return
	}
	respond(w, 200, profile)
}

func (s *Server) updateProfile(w http.ResponseWriter, r *http.Request) {
	var input profileRequest
	if !decodeProfile(w, r, &input, true) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	current, ok := s.profiles[id]
	if !ok {
		fail(w, 404, "profile not found")
		return
	}
	if input.ExpectedRevision != current.Revision {
		fail(w, 409, "profile revision conflict")
		return
	}
	updated := input.profile()
	updated.ID, updated.Revision = id, current.Revision+1
	profiles := copyProfiles(s.profiles)
	profiles[id] = updated
	if err := s.saveProfiles(profiles); err != nil {
		fail(w, 500, "could not persist profile")
		return
	}
	respond(w, 200, updated)
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision int `json:"revision"`
	}
	if !decodeBounded(w, r, &input, 64<<10) {
		return
	}
	if input.Revision < 1 {
		fail(w, 400, "positive revision is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	current, ok := s.profiles[id]
	if !ok {
		fail(w, 404, "profile not found")
		return
	}
	if input.Revision != current.Revision {
		fail(w, 409, "profile revision conflict")
		return
	}
	for _, project := range s.projects {
		if project.ProfileID == id {
			fail(w, 409, "profile is referenced by a project")
			return
		}
	}

	profiles := copyProfiles(s.profiles)
	delete(profiles, id)
	if err := s.saveProfiles(profiles); err != nil {
		fail(w, 500, "could not persist profile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func copyProfiles(profiles map[string]MachineProfile) map[string]MachineProfile {
	copy := make(map[string]MachineProfile, len(profiles))
	for id, profile := range profiles {
		copy[id] = profile
	}
	return copy
}
