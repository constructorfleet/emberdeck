// Package server exposes EmberDeck's durable job management HTTP API.
package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/jobtransfer"
	"github.com/ConstructorFleet/emberdeck/plugin"
	"github.com/gofrs/flock"
)

// Server serializes durable mutations before acknowledging API requests.
type Server struct {
	storageErr  error
	lock        *flock.Flock
	closed      bool
	plugins     []plugin.Plugin
	drivers     map[string]Driver
	credentials map[string]credentialRecord
	mu          sync.Mutex
	dir         string
	key         string
	jobs        map[string]jobRecord
	// progressDirty marks in-memory progress newer than jobs.json. Progress-only
	// reports are not written; the next durable write or Close persists them.
	progressDirty bool
	profiles      map[string]MachineProfile
	projects      map[string]DesignerProject
	mux           *http.ServeMux
	metrics       *serverMetrics
	history       eventHistory
	logger        *slog.Logger
}

// Options configures a server beyond its data directory and API key.
type Options struct {
	// PluginDir optionally names a directory of trusted preflight plugins.
	PluginDir string
	// Logger receives request, job transition, and storage logs, including
	// restart recoveries during Open. Nil discards them; SetLogger can
	// replace it later.
	Logger *slog.Logger
	// EventLimit is the maximum number of retained job events. Zero uses
	// DefaultEventLimit; history is always bounded.
	EventLimit int
	// EventRetention is the maximum age of retained job events. Zero keeps
	// events until EventLimit removes them.
	EventRetention time.Duration
}

// Open owns the data directory and loads its job, profile, and project libraries.
func Open(dir, key string) (*Server, error) {
	return OpenWithOptions(dir, key, Options{})
}

// OpenWithOptions opens a server with plugins, logging, and job event
// retention. Plugin discovery and every library load fail closed.
func OpenWithOptions(dir, key string, options Options) (*Server, error) {
	if key == "" {
		return nil, errors.New("API key is required")
	}
	if options.EventLimit < 0 || options.EventRetention < 0 {
		return nil, errors.New("job event retention must not be negative")
	}
	if options.EventLimit == 0 {
		options.EventLimit = DefaultEventLimit
	}
	var plugins []plugin.Plugin
	if options.PluginDir != "" {
		var err error
		if plugins, err = plugin.Discover(options.PluginDir); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dir, "server.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("data directory is already owned by another server")
	}
	opened := false
	defer func() {
		if !opened {
			_ = lock.Close()
		}
	}()
	s := &Server{lock: lock, drivers: make(map[string]Driver), dir: dir, key: key, plugins: plugins, profiles: make(map[string]MachineProfile), projects: make(map[string]DesignerProject), mux: http.NewServeMux(), logger: defaultLogger(options.Logger)}
	s.history.limit, s.history.retention = options.EventLimit, options.EventRetention
	s.metrics = newServerMetrics(s)
	defer func() {
		if !opened {
			_ = s.history.close()
		}
	}()
	if err := s.loadJobs(); err != nil {
		return nil, err
	}
	if err := loadOptionalJSON(filepath.Join(dir, "profiles.json"), &s.profiles); err != nil {
		return nil, err
	}
	if s.profiles == nil {
		return nil, errors.New("invalid null profile library")
	}
	for id, profile := range s.profiles {
		if !validHexID(id) || profile.ID != id || profile.Revision < 1 || !validProfileValues(profile) {
			return nil, errors.New("invalid machine profile library entry")
		}
	}
	if err := loadOptionalJSON(filepath.Join(dir, "designs.json"), &s.projects); err != nil {
		return nil, err
	}
	if s.projects == nil {
		return nil, errors.New("invalid null design library")
	}
	for id, project := range s.projects {
		if !validHexID(id) || project.ID != id || project.Revision < 1 || !validStoredProject(project, s.profiles) {
			return nil, errors.New("invalid designer project library entry")
		}
	}
	if err := s.loadCredentials(key); err != nil {
		return nil, err
	}
	if err := s.populateMissingReviews(); err != nil {
		return nil, err
	}
	if err := s.recoverInterrupted(); err != nil {
		return nil, err
	}
	s.metrics.syncJobStates(s.jobs)
	readRoles := []string{"viewer", "operator", "admin"}
	writeRoles := []string{"operator", "admin"}
	adminRoles := []string{"admin"}
	s.handleAPI("GET /api/v1/auth", readRoles, false, s.authInfo)
	s.handleAPI("GET /api/v1/credentials", adminRoles, false, s.listCredentials)
	s.handleAPI("POST /api/v1/credentials", adminRoles, false, s.createCredential)
	s.handleAPI("DELETE /api/v1/credentials/{id}", adminRoles, false, s.revokeCredential)
	s.handleAPI("GET /api/v1/drivers", readRoles, false, s.listDrivers)
	// Admin includes a compatibility path for the bootstrap/local worker that
	// historically shared EMBERDECK_API_KEY. Dedicated worker keys remain scoped.
	s.handleAPI("POST /api/v1/drivers/{id}/poll", []string{"worker", "admin"}, true, s.pollDriver)
	s.handleAPI("GET /api/v1/drivers/{id}/jobs/{job_id}/content", []string{"worker", "admin"}, true, s.getWorkerContent)
	s.handleAPI("POST /api/v1/jobs/{id}/start", writeRoles, false, s.startJob)
	s.handleAPI("POST /api/v1/jobs/{id}/cancel", writeRoles, false, s.cancelJob)
	s.handleAPI("POST /api/v1/jobs/{id}/pause", writeRoles, false, s.pauseJob)
	s.handleAPI("POST /api/v1/jobs/{id}/resume", writeRoles, false, s.resumeJob)
	s.handleAPI("POST /api/v1/jobs", writeRoles, false, s.upload)
	s.handleAPI("GET /api/v1/jobs", readRoles, false, s.list)
	s.handleAPI("GET /api/v1/jobs/{id}/toolpath", readRoles, false, s.getToolpath)
	s.handleAPI("GET /api/v1/jobs/{id}/review", readRoles, false, s.getJobReview)
	s.handleAPI("GET /api/v1/jobs/{id}/events", readRoles, false, s.listEvents)
	s.handleAPI("GET /api/v1/jobs/{id}", readRoles, false, s.get)
	s.handleAPI("DELETE /api/v1/jobs/{id}", writeRoles, false, s.delete)
	s.handleAPI("GET /api/v1/profiles", readRoles, false, s.listProfiles)
	s.handleAPI("POST /api/v1/profiles", writeRoles, false, s.createProfile)
	s.handleAPI("GET /api/v1/profiles/{id}", readRoles, false, s.getProfile)
	s.handleAPI("PUT /api/v1/profiles/{id}", writeRoles, false, s.updateProfile)
	s.handleAPI("DELETE /api/v1/profiles/{id}", writeRoles, false, s.deleteProfile)
	s.handleAPI("GET /api/v1/projects", readRoles, false, s.listProjects)
	s.handleAPI("POST /api/v1/projects", writeRoles, false, s.createProject)
	s.handleAPI("POST /api/v1/svg-import", writeRoles, false, s.importSVG)
	s.handleAPI("GET /api/v1/projects/{id}", readRoles, false, s.getProject)
	s.handleAPI("PUT /api/v1/projects/{id}", writeRoles, false, s.updateProject)
	s.handleAPI("DELETE /api/v1/projects/{id}", writeRoles, false, s.deleteProject)
	s.handleAPI("POST /api/v1/projects/{id}/generate", writeRoles, false, s.generateProject)
	s.handleAPI("GET /api/v1/plugins", readRoles, false, s.listPlugins)
	opened = true
	return s, nil
}

// populateMissingReviews reviews uploaded jobs stored before program review
// existed, including migrated libraries. Programs are read from the store.
func (s *Server) populateMissingReviews() error {
	var jobs map[string]jobRecord
	for id, record := range s.jobs {
		if record.Provenance != nil || (record.Review != nil && record.Review.ConstantPowerM3 != nil) {
			continue
		}
		gcode, err := s.readProgram(record)
		if err != nil {
			return fmt.Errorf("review job %s: %w", id, err)
		}
		if record.Review != nil {
			// Reviews stored before the M3 check gain only that flag.
			review := *record.Review
			review.ConstantPowerM3 = constantPowerFlag(gcode)
			if jobs == nil {
				jobs = s.copyJobs()
			}
			record.Review = &review
			jobs[id] = record
			continue
		}
		var profile *MachineProfile
		stale := false
		if selected, ok := s.profiles[record.ProfileID]; ok {
			profile = &selected
			stale = selected.Revision != record.ProfileRevision
		}
		review := reviewProgram(gcode, profile, stale)
		if jobs == nil {
			jobs = s.copyJobs()
		}
		record.Review = &review
		jobs[id] = record
	}
	if jobs != nil {
		return s.save(jobs, transitionCause{})
	}
	return nil
}

func validHexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	tracked := &statusRecorder{ResponseWriter: w}
	defer func() {
		status := tracked.status
		if status == 0 {
			status = http.StatusOK
		}
		elapsed := time.Since(started)
		s.metrics.observeHTTP(r.Method, status, elapsed)
		s.logRequest(r, status, elapsed)
	}()
	w = tracked
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.serveUI(w, r) {
		return
	}
	// Only the API namespace is authenticated. Unknown pages are ordinary
	// misses so a mistyped URL never looks like a rejected API key.
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	r, authenticated := s.authenticate(w, r)
	if !authenticated {
		return
	}
	s.mu.Lock()
	unavailable := s.closed || s.storageErr != nil
	s.mu.Unlock()
	if unavailable {
		fail(w, 503, "storage unavailable; stop and inspect server before restarting")
		return
	}
	s.mux.ServeHTTP(w, r)
}

func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string `json:"name"`
		Gcode     string `json:"gcode"`
		ProfileID string `json:"profile_id"`
	}
	body := http.MaxBytesReader(w, r.Body, jobtransfer.MaxUploadJSONBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(w, 400, "invalid job JSON (program maximum 4 MiB; upload JSON maximum 24 MiB plus 64 KiB)")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		fail(w, 400, "expected one JSON object")
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 200 || strings.TrimSpace(input.Gcode) == "" || strings.ContainsFunc(input.Gcode, func(r rune) bool { return (r < 32 && r != '\n' && r != '\r' && r != '\t') || r == 127 }) {
		fail(w, 400, "name and text G-code are required")
		return
	}
	if len(input.Gcode) > jobtransfer.MaxProgramBytes {
		fail(w, http.StatusRequestEntityTooLarge, "job program exceeds the 4 MiB maximum")
		return
	}
	total, err := driver.Validate(input.Gcode)
	if err != nil {
		fail(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	var profile *MachineProfile
	if input.ProfileID != "" {
		s.mu.Lock()
		selected, exists := s.profiles[input.ProfileID]
		s.mu.Unlock()
		if !exists {
			fail(w, http.StatusBadRequest, "selected profile does not exist")
			return
		}
		profile = &selected
	}
	id, err := randomID()
	if err != nil {
		fail(w, 500, "could not allocate job ID")
		return
	}
	// The review parses the whole program, so it runs outside the mutex. It
	// records the profile revision; a later profile change marks it stale.
	review := reviewProgram(input.Gcode, profile, false)
	job := Job{ID: id, Name: input.Name, State: "queued", Created: time.Now().UTC(), Total: total, Review: &review}
	if profile != nil {
		job.ProfileID, job.ProfileRevision = profile.ID, profile.Revision
	}
	job, ok := s.addJob(w, job, input.Gcode, operatorCause(r, "upload"))
	if !ok {
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	respond(w, 201, job)
}

// addJob stores the program file outside the mutex, then publishes metadata.
// It writes the failure response itself and reports whether the job exists.
func (s *Server) addJob(w http.ResponseWriter, job Job, gcode string, cause transitionCause) (Job, bool) {
	if len(gcode) > jobtransfer.MaxProgramBytes {
		fail(w, http.StatusRequestEntityTooLarge, "job program exceeds the 4 MiB maximum")
		return job, false
	}
	job.Size = int64(len(gcode))
	if err := s.writeProgram(job.ID, gcode); err != nil {
		fail(w, 500, "could not persist job")
		return job, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := s.copyJobs()
	jobs[job.ID] = jobRecord{Job: job, SHA256: programDigest(gcode)}
	if err := s.save(jobs, cause); err != nil {
		_ = os.Remove(s.programPath(job.ID))
		fail(w, 500, "could not persist job")
		return job, false
	}
	return job, true
}

const (
	defaultJobPageSize = 50
	maxJobPageSize     = 200
)

// list returns one page of job metadata, newest first. Program bytes and
// leases are never included, so the response size is bounded by the page.
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit, offset := defaultJobPageSize, 0
	if value := query.Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maxJobPageSize {
			fail(w, 400, "limit must be an integer from 1 to 200")
			return
		}
		limit = parsed
	}
	if value := query.Get("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			fail(w, 400, "offset must be a non-negative integer")
			return
		}
		offset = parsed
	}
	states := map[string]bool{}
	for _, value := range query["state"] {
		for _, state := range strings.Split(value, ",") {
			if !knownJobState(state) {
				fail(w, 400, "unknown job state filter")
				return
			}
			states[state] = true
		}
	}
	driverID := query.Get("driver")
	if driverID != "" && !driverName.MatchString(driverID) {
		fail(w, 400, "invalid driver filter")
		return
	}
	name := strings.ToLower(strings.TrimSpace(query.Get("name")))
	if len(name) > 200 {
		fail(w, 400, "name filter is too long")
		return
	}
	s.mu.Lock()
	if err := s.expire(); err != nil {
		s.mu.Unlock()
		fail(w, 500, "could not persist driver expiration")
		return
	}
	jobs := make([]Job, 0, len(s.jobs))
	for _, record := range s.jobs {
		j := record.Job
		if (len(states) > 0 && !states[j.State]) || (driverID != "" && j.DriverID != driverID) || (name != "" && !strings.Contains(strings.ToLower(j.Name), name)) {
			continue
		}
		j.Review = s.currentReview(j)
		jobs = append(jobs, j)
	}
	s.mu.Unlock()
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Created.Equal(jobs[j].Created) {
			return jobs[i].ID > jobs[j].ID
		}
		return jobs[i].Created.After(jobs[j].Created)
	})
	total := len(jobs)
	page := jobs[min(offset, total):min(offset+limit, total)]
	respond(w, 200, map[string]any{"jobs": page, "total": total, "offset": offset, "limit": limit})
}

// lookup returns a job's metadata record after applying driver expiration.
func (s *Server) lookup(w http.ResponseWriter, id string) (jobRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return jobRecord{}, false
	}
	record, ok := s.jobs[id]
	if !ok {
		fail(w, 404, "job not found")
		return jobRecord{}, false
	}
	record.Review = s.currentReview(record.Job)
	return record, true
}

// program reads a job's stored bytes without holding the server mutex.
func (s *Server) program(w http.ResponseWriter, record jobRecord) (string, bool) {
	gcode, err := s.readProgram(record)
	if errors.Is(err, errProgramMissing) {
		fail(w, 404, "job not found")
		return "", false
	}
	if err != nil {
		fail(w, 500, "could not read stored job program")
		return "", false
	}
	return gcode, true
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	record, ok := s.lookup(w, r.PathValue("id"))
	if !ok {
		return
	}
	gcode, ok := s.program(w, record)
	if !ok {
		return
	}
	respond(w, 200, jobDetail{Job: record.Job, Gcode: gcode})
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.jobs[id]; !ok {
		fail(w, 404, "job not found")
		return
	}
	if activeJob(s.jobs[id].State) {
		fail(w, 409, "cancel active job before deleting")
		return
	}
	jobs := s.copyJobs()
	delete(jobs, id)
	if err := s.save(jobs, operatorCause(r, "delete")); err != nil {
		fail(w, 500, "could not persist deletion")
		return
	}
	// Metadata no longer names the file; startup removes it if this fails.
	_ = os.Remove(s.programPath(id))
	w.WriteHeader(204)
}

// Close releases ownership after the HTTP server and workers have stopped.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	var flushErr error
	if s.progressDirty && s.storageErr == nil {
		// Keep the latest reported progress after a graceful shutdown.
		flushErr = s.save(s.copyJobs(), transitionCause{})
	}
	s.closed = true
	return errors.Join(flushErr, s.history.close(), s.lock.Close())
}
