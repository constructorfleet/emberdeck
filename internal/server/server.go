// Package server exposes EmberDeck's durable job management HTTP API.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ConstructorFleet/emberdeck/plugin"
	"github.com/gofrs/flock"
)

// Job is a durable record. Gcode is the exact uploaded machine program.
type Job struct {
	DriverID string    `json:"driver_id,omitempty"`
	Lease    string    `json:"lease,omitempty"`
	Acked    int       `json:"acked"`
	Total    int       `json:"total"`
	Error    string    `json:"error,omitempty"`
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Gcode    string    `json:"gcode"`
	State    string    `json:"state"`
	Created  time.Time `json:"created"`
}

// Server serializes durable mutations before acknowledging API requests.
type Server struct {
	lock    *flock.Flock
	closed  bool
	plugins []plugin.Plugin
	drivers map[string]Driver
	mu      sync.Mutex
	dir     string
	key     string
	jobs    map[string]Job
	mux     *http.ServeMux
}

// Open loads an existing job library or creates an empty one.
func Open(dir, key string) (*Server, error) {
	if key == "" {
		return nil, errors.New("API key is required")
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
	s := &Server{lock: lock, drivers: make(map[string]Driver), dir: dir, key: key, jobs: make(map[string]Job), mux: http.NewServeMux()}
	data, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if err == nil {
		if err = json.Unmarshal(data, &s.jobs); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.jobs == nil {
		return nil, errors.New("invalid null job library")
	}
	if err := s.recoverInterrupted(); err != nil {
		return nil, err
	}
	s.mux.HandleFunc("GET /api/v1/drivers", s.listDrivers)
	s.mux.HandleFunc("POST /api/v1/drivers/{id}/poll", s.pollDriver)
	s.mux.HandleFunc("POST /api/v1/jobs/{id}/start", s.startJob)
	s.mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.cancelJob)
	s.mux.HandleFunc("POST /api/v1/jobs", s.upload)
	s.mux.HandleFunc("GET /api/v1/jobs", s.list)
	s.mux.HandleFunc("GET /api/v1/jobs/{id}", s.get)
	s.mux.HandleFunc("DELETE /api/v1/jobs/{id}", s.delete)
	opened = true
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.serveUI(w, r) {
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.key)) != 1 {
		fail(w, 401, "authentication required")
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

func (s *Server) save(jobs map[string]Job) error {
	if s.closed {
		return errors.New("server is closed")
	}
	data, err := json.Marshal(jobs)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".jobs-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, "jobs.json")); err != nil {
		return err
	}
	s.jobs = jobs
	return nil
}
func (s *Server) copyJobs() map[string]Job {
	jobs := make(map[string]Job, len(s.jobs))
	for id, job := range s.jobs {
		jobs[id] = job
	}
	return jobs
}
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name  string `json:"name"`
		Gcode string `json:"gcode"`
	}
	body := http.MaxBytesReader(w, r.Body, 4<<20)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		fail(w, 400, "invalid job JSON (maximum 4 MiB)")
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
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		fail(w, 500, "could not allocate job ID")
		return
	}
	job := Job{ID: hex.EncodeToString(id), Name: input.Name, Gcode: input.Gcode, State: "queued", Created: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := s.copyJobs()
	jobs[job.ID] = job
	if err := s.save(jobs); err != nil {
		fail(w, 500, "could not persist job")
		return
	}
	w.Header().Set("Location", "/api/v1/jobs/"+job.ID)
	respond(w, 201, job)
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return
	}
	jobs := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].Created.Equal(jobs[j].Created) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].Created.Before(jobs[j].Created)
	})
	respond(w, 200, jobs)
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return
	}
	job, ok := s.jobs[r.PathValue("id")]
	if !ok {
		fail(w, 404, "job not found")
		return
	}
	respond(w, 200, job)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if _, ok := s.jobs[id]; !ok {
		fail(w, 404, "job not found")
		return
	}
	if s.jobs[id].State == "running" || s.jobs[id].State == "canceling" {
		fail(w, 409, "cancel active job before deleting")
		return
	}
	jobs := s.copyJobs()
	delete(jobs, id)
	if err := s.save(jobs); err != nil {
		fail(w, 500, "could not persist deletion")
		return
	}
	w.WriteHeader(204)
}

// Close releases ownership after the HTTP server and workers have stopped.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.lock.Close()
}
