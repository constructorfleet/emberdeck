package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ConstructorFleet/emberdeck/driver"
	"github.com/ConstructorFleet/emberdeck/jobtransfer"
	"github.com/ConstructorFleet/emberdeck/toolpath"
)

// Job is operator-visible job metadata. Program bytes are returned only by the
// single-job endpoint, and the worker lease is never returned to operators.
type Job struct {
	ControllerState string              `json:"controller_state,omitempty"`
	DriverID        string              `json:"driver_id,omitempty"`
	Acked           int                 `json:"acked"`
	Total           int                 `json:"total"`
	Size            int64               `json:"size"`
	Error           string              `json:"error,omitempty"`
	ID              string              `json:"id"`
	Name            string              `json:"name"`
	State           string              `json:"state"`
	Created         time.Time           `json:"created"`
	Started         *time.Time          `json:"started,omitempty"`
	Ended           *time.Time          `json:"ended,omitempty"`
	DurationMS      *int64              `json:"duration_ms,omitempty"`
	ProfileID       string              `json:"profile_id,omitempty"`
	ProfileRevision int                 `json:"profile_revision,omitempty"`
	Provenance      *JobProvenance      `json:"provenance,omitempty"`
	Execution       *toolpath.Execution `json:"execution,omitempty"`
	Review          *ProgramReview      `json:"program_review,omitempty"`
}

// jobDetail is the single-job response: metadata plus exact program bytes.
type jobDetail struct {
	Job
	Gcode string `json:"gcode"`
}

// workerJob is the worker-facing assignment. Gcode is sent only when a worker
// picks up an assignment, not with every progress acknowledgement.
type workerJob struct {
	Job
	Lease string `json:"lease"`
	Gcode string `json:"gcode,omitempty"`
	// ExpectedSettings is the profile snapshot's $30/$31/$32 for generated jobs.
	ExpectedSettings *driver.Settings      `json:"expected_settings,omitempty"`
	Content          *jobtransfer.Manifest `json:"content,omitempty"`
}

// jobRecord is one durable metadata entry in jobs.json (format version 3).
type jobRecord struct {
	Job
	Lease  string `json:"lease,omitempty"`
	SHA256 string `json:"sha256"`
	// segments is the toolpath segment count, derived at dispatch for telemetry
	// validation so progress reports never re-read the program.
	segments int
}

// jobIndex is jobs.json. EventSeq is the last job event committed with this
// index; the event log may hold later, uncommitted events after a crash.
type jobIndex struct {
	Version  int                  `json:"version"`
	EventSeq int64                `json:"event_seq"`
	Jobs     map[string]jobRecord `json:"jobs"`
}

// legacyJob is the version 1 jobs.json entry that embedded program bytes.
type legacyJob struct {
	Job
	Lease string `json:"lease"`
	Gcode string `json:"gcode"`
}

// Version 3 adds the committed event sequence. Version 2 libraries load
// with no history and are rewritten as version 3 on the next save.
const jobIndexVersion = 3

var (
	errProgramMissing = errors.New("job program is missing")
	errProgramCorrupt = errors.New("job program does not match its recorded size and digest")
	programFileName   = regexp.MustCompile(`^[0-9a-f]{32}\.gcode$`)
)

func knownJobState(state string) bool {
	switch state {
	case "queued", "completed", "failed", "canceled", "interrupted":
		return true
	default:
		return activeJob(state)
	}
}

func (s *Server) programDir() string           { return filepath.Join(s.dir, "programs") }
func (s *Server) programPath(id string) string { return filepath.Join(s.programDir(), id+".gcode") }

func programDigest(gcode string) string {
	sum := sha256.Sum256([]byte(gcode))
	return hex.EncodeToString(sum[:])
}

// writeProgram durably stores one program file. It does not need the server
// mutex: IDs are unique and the file is invisible until metadata names it.
func (s *Server) writeProgram(id, gcode string) error {
	f, err := os.CreateTemp(s.programDir(), ".program-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(gcode); err != nil {
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
	if err = os.Rename(f.Name(), s.programPath(id)); err != nil {
		return err
	}
	return syncDirectory(s.programDir())
}

// readProgram returns the exact stored bytes after checking size and digest.
func (s *Server) readProgram(record jobRecord) (string, error) {
	data, err := os.ReadFile(s.programPath(record.ID))
	if errors.Is(err, os.ErrNotExist) {
		return "", errProgramMissing
	}
	if err != nil {
		return "", err
	}
	gcode := string(data)
	if int64(len(data)) != record.Size || programDigest(gcode) != record.SHA256 {
		return "", errProgramCorrupt
	}
	return gcode, nil
}

// loadJobs reads the metadata index, migrating a version 1 library first.
// Every failure leaves the existing jobs.json untouched and aborts startup.
func (s *Server) loadJobs() error {
	if err := os.MkdirAll(s.programDir(), 0700); err != nil {
		return fmt.Errorf("job program store: %w", err)
	}
	path := filepath.Join(s.dir, "jobs.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.jobs = make(map[string]jobRecord)
		if err := s.loadHistory(0); err != nil {
			return err
		}
		return s.removeOrphanPrograms()
	}
	if err != nil {
		return err
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return fmt.Errorf("job library: %w", err)
	}
	if probe == nil {
		return errors.New("invalid null job library")
	}
	if _, versioned := probe["version"]; !versioned {
		return s.migrateLegacyJobs(data)
	}
	var index jobIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return fmt.Errorf("job library: %w", err)
	}
	if (index.Version != jobIndexVersion && index.Version != 2) || index.Jobs == nil || index.EventSeq < 0 || (index.Version == 2 && index.EventSeq != 0) {
		return fmt.Errorf("unsupported job library version %d", index.Version)
	}
	for id, record := range index.Jobs {
		if !validHexID(id) || record.ID != id || record.Name == "" || !knownJobState(record.State) || record.Size <= 0 || len(record.SHA256) != 64 {
			return fmt.Errorf("invalid job library entry %q", id)
		}
		info, err := os.Stat(s.programPath(id))
		if err != nil || !info.Mode().IsRegular() || info.Size() != record.Size {
			return fmt.Errorf("job %s program file is missing or damaged", id)
		}
	}
	if err := s.loadHistory(index.EventSeq); err != nil {
		return err
	}
	s.jobs = index.Jobs
	return s.removeOrphanPrograms()
}

// migrateLegacyJobs moves embedded programs into per-job files, then commits
// the metadata-only index by atomically replacing jobs.json. A crash before
// the rename leaves the legacy library authoritative and migration reruns.
func (s *Server) migrateLegacyJobs(data []byte) error {
	var legacy map[string]legacyJob
	if err := json.Unmarshal(data, &legacy); err != nil {
		return fmt.Errorf("legacy job library: %w", err)
	}
	jobs := make(map[string]jobRecord, len(legacy))
	for id, old := range legacy {
		if !validHexID(id) || old.ID != id || strings.TrimSpace(old.Name) == "" || strings.TrimSpace(old.Gcode) == "" || !knownJobState(old.State) {
			return fmt.Errorf("invalid legacy job library entry %q", id)
		}
		record := jobRecord{Job: old.Job, Lease: old.Lease, SHA256: programDigest(old.Gcode)}
		record.Size = int64(len(old.Gcode))
		if !activeJob(record.State) {
			record.Lease = ""
		}
		jobs[id] = record
	}
	for id, old := range legacy {
		if err := s.writeProgram(id, old.Gcode); err != nil {
			return fmt.Errorf("migrate job %s program: %w", id, err)
		}
	}
	if err := s.loadHistory(0); err != nil {
		return err
	}
	// Migration is not a lifecycle transition; compare against the same
	// states so existing jobs are not counted as new in metrics.
	s.jobs = jobs
	if err := s.save(s.copyJobs(), transitionCause{}); err != nil {
		return fmt.Errorf("commit migrated job library: %w", err)
	}
	return s.removeOrphanPrograms()
}

// removeOrphanPrograms deletes program files no metadata entry names, such as
// leftovers from an upload or deletion interrupted by a crash.
func (s *Server) removeOrphanPrograms() error {
	entries, err := os.ReadDir(s.programDir())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		orphan := strings.HasPrefix(name, ".program-") && strings.HasSuffix(name, ".tmp")
		if programFileName.MatchString(name) {
			_, known := s.jobs[strings.TrimSuffix(name, ".gcode")]
			orphan = !known
		}
		if orphan {
			if err := os.Remove(filepath.Join(s.programDir(), name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// save durably replaces the metadata index. It is the only place job state
// changes are committed, so it derives the event history from the state
// difference: events are appended to the log first, then the index rename
// commits them together with the new states. Callers hold the server mutex.
func (s *Server) save(jobs map[string]jobRecord, cause transitionCause) error {
	if s.closed {
		return errors.New("server is closed")
	}
	if s.storageErr != nil {
		return s.storageErr
	}
	now := time.Now().UTC()
	events := s.transitionEvents(jobs, cause, now)
	eventSeq := s.history.seq
	if len(events) > 0 {
		eventSeq = events[len(events)-1].Seq
	}
	data, err := json.Marshal(jobIndex{Version: jobIndexVersion, EventSeq: eventSeq, Jobs: jobs})
	if err != nil {
		return err
	}
	rollback := int64(-1)
	if len(events) > 0 {
		if rollback, err = s.appendEvents(events); err != nil {
			return err
		}
	}
	if err := s.writeIndex(data); err != nil {
		if rollback >= 0 {
			s.rollbackEvents(rollback)
		}
		return err
	}
	if err := syncDirectory(s.dir); err != nil {
		// Rename is already visible. Reject all subsequent API work so no uncertain
		// assignment can be dispatched from either old or new in-memory state.
		s.failStorage(err)
		return err
	}
	for id, job := range jobs {
		previous, exists := s.jobs[id]
		from := "new"
		if exists {
			from = previous.State
		}
		if from != job.State && (from == "new" || isMetricJobState(from)) && isMetricJobState(job.State) {
			s.metrics.transitions.WithLabelValues(from, job.State).Inc()
		}
	}
	previous := s.jobs
	s.jobs = jobs
	s.progressDirty = false
	s.metrics.syncJobStates(jobs)
	s.commitEvents(events, previous, now)
	return nil
}

// writeIndex writes jobs.json through a synced temporary file and rename.
func (s *Server) writeIndex(data []byte) error {
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
	return os.Rename(f.Name(), filepath.Join(s.dir, "jobs.json"))
}

func isMetricJobState(state string) bool {
	for _, known := range metricJobStates {
		if state == known {
			return true
		}
	}
	return false
}

func (s *Server) copyJobs() map[string]jobRecord {
	jobs := make(map[string]jobRecord, len(s.jobs))
	for id, job := range s.jobs {
		jobs[id] = job
	}
	return jobs
}
