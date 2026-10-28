package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultEventLimit bounds the retained job event history when Options does
// not set a limit. History is never unbounded.
const DefaultEventLimit = 50000

const (
	eventLogName    = "job-events.jsonl"
	eventLogFormat  = "emberdeck-job-events"
	eventLogVersion = 1
)

// JobEvent is one append-only job history entry. Session and lease values are
// worker credentials, so events carry only opaque digests that correlate
// events without allowing a worker to be impersonated.
type JobEvent struct {
	Seq    int64     `json:"seq"`
	Time   time.Time `json:"time"`
	JobID  string    `json:"job_id"`
	From   string    `json:"from,omitempty"`
	To     string    `json:"to"`
	Source string    `json:"source"`
	Action string    `json:"action"`
	// CredentialID and CredentialName identify the API credential whose
	// request caused the change. Server-originated events have none.
	CredentialID   string `json:"credential_id,omitempty"`
	CredentialName string `json:"credential_name,omitempty"`
	DriverID       string `json:"driver_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	LeaseID        string `json:"lease_id,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

// transitionCause says who or what caused the transitions committed by one
// save. Session is the raw worker session when a worker reported them.
type transitionCause struct {
	source  string
	action  string
	session string
	actor   principal
}

func operatorCause(r *http.Request, action string) transitionCause {
	return transitionCause{source: "operator", action: action, actor: requestPrincipal(r)}
}

func workerCause(r *http.Request, session string) transitionCause {
	return transitionCause{source: "worker", action: "report", session: session, actor: requestPrincipal(r)}
}

// eventLogHeader is the first line of the event log. PrunedThrough is the
// committed sequence when the log was last compacted; it lets startup tell
// pruned history apart from lost history.
type eventLogHeader struct {
	Format        string `json:"format"`
	Version       int    `json:"version"`
	PrunedThrough int64  `json:"pruned_through"`
}

// eventHistory is the in-memory view and append handle of job-events.jsonl.
// Callers hold the server mutex.
type eventHistory struct {
	path      string
	file      *os.File
	size      int64
	events    []JobEvent
	seq       int64 // last committed sequence
	inFile    int   // event lines in the log, including pruned ones
	limit     int
	retention time.Duration
}

func opaqueRef(kind, secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("emberdeck-" + kind + ":" + secret))
	return hex.EncodeToString(sum[:8])
}

func validEventSource(source string) bool {
	return source == "operator" || source == "worker" || source == "server"
}

// loadHistory opens the event log and reconciles it with the committed
// sequence recorded in jobs.json. Missing or damaged committed history aborts
// startup without rewriting anything. Complete or torn events beyond the
// committed sequence come from a save that crashed before jobs.json was
// replaced; they never happened and are truncated.
func (s *Server) loadHistory(committed int64) error {
	h := &s.history
	h.path = filepath.Join(s.dir, eventLogName)
	data, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		if committed > 0 {
			return fmt.Errorf("job event history %s is missing", eventLogName)
		}
		h.seq = 0
		return s.rewriteHistory(nil, 0)
	}
	if err != nil {
		return fmt.Errorf("job event history: %w", err)
	}
	newline := bytes.IndexByte(data, '\n')
	var header eventLogHeader
	if newline < 0 || json.Unmarshal(data[:newline], &header) != nil || header.Format != eventLogFormat || header.Version != eventLogVersion || header.PrunedThrough < 0 || header.PrunedThrough > committed {
		return fmt.Errorf("job event history %s has an invalid header", eventLogName)
	}
	offset := int64(newline + 1)
	last := header.PrunedThrough
	events := []JobEvent{}
	for offset < int64(len(data)) {
		rest := data[offset:]
		end := bytes.IndexByte(rest, '\n')
		var event JobEvent
		valid := end >= 0 && json.Unmarshal(rest[:end], &event) == nil && event.Seq > last && validHexID(event.JobID) && event.To != "" && validEventSource(event.Source) && !event.Time.IsZero()
		if !valid || event.Seq > committed {
			if last < committed {
				return fmt.Errorf("job event history is damaged before committed event %d", committed)
			}
			break // uncommitted tail
		}
		events = append(events, event)
		last = event.Seq
		offset += int64(end + 1)
	}
	if last != committed {
		return fmt.Errorf("job event history ends at event %d, but the library committed event %d", last, committed)
	}
	if offset < int64(len(data)) {
		if err := truncateFile(h.path, offset); err != nil {
			return fmt.Errorf("discard uncommitted job events: %w", err)
		}
		s.logger.Warn("discarded uncommitted job events after an interrupted save", "bytes", int64(len(data))-offset)
	}
	h.events, h.seq, h.inFile = events, committed, len(events)
	h.prune(time.Now().UTC())
	if len(h.events) == h.inFile {
		return h.open(offset)
	}
	return s.rewriteHistory(h.events, committed)
}

func truncateFile(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (h *eventHistory) open(size int64) error {
	// Not O_APPEND: Windows cannot truncate an append-only handle, and
	// rollback must truncate. Appends write at the tracked size instead.
	f, err := os.OpenFile(h.path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if h.file != nil {
		_ = h.file.Close()
	}
	h.file, h.size = f, size
	return nil
}

// rewriteHistory atomically replaces the log with the retained events.
func (s *Server) rewriteHistory(events []JobEvent, prunedThrough int64) error {
	h := &s.history
	var buf bytes.Buffer
	header, _ := json.Marshal(eventLogHeader{Format: eventLogFormat, Version: eventLogVersion, PrunedThrough: prunedThrough})
	buf.Write(header)
	buf.WriteByte('\n')
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	f, err := os.CreateTemp(s.dir, ".job-events-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(buf.Bytes()); err != nil {
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
	// Windows cannot replace a file that is still open, so release the
	// append handle first and restore it if the replacement fails.
	if h.file != nil {
		_ = h.file.Close()
		h.file = nil
	}
	if err = os.Rename(f.Name(), h.path); err != nil {
		if h.path != "" && h.size > 0 {
			if reopenErr := h.open(h.size); reopenErr != nil {
				s.storageErr = reopenErr
			}
		}
		return err
	}
	if err := syncDirectory(s.dir); err != nil {
		s.failStorage(err)
		return err
	}
	h.inFile = len(events)
	if err := h.open(int64(buf.Len())); err != nil {
		// The append handle still names the replaced file.
		s.failStorage(err)
		return err
	}
	return nil
}

// transitionEvents derives one event per job whose state differs between the
// committed library and jobs, and stamps start and end times into jobs.
func (s *Server) transitionEvents(jobs map[string]jobRecord, cause transitionCause, now time.Time) []JobEvent {
	if cause.source == "" {
		cause = transitionCause{source: "server", action: "recovery"}
	}
	var events []JobEvent
	add := func(id string, previous, current jobRecord, from, to string) {
		driverID := current.DriverID
		if driverID == "" {
			driverID = previous.DriverID
		}
		lease := current.Lease
		if lease == "" {
			lease = previous.Lease
		}
		event := JobEvent{Time: now, JobID: id, From: from, To: to, Source: cause.source, Action: cause.action, DriverID: driverID, CredentialID: cause.actor.ID, CredentialName: cause.actor.Name}
		if driverID != "" && (lease != "" || activeJob(from) || activeJob(to)) {
			session := cause.session
			if session == "" {
				session = s.drivers[driverID].Session
			}
			event.SessionID = opaqueRef("session", session)
			event.LeaseID = opaqueRef("lease", lease)
		}
		if to == "failed" || to == "interrupted" {
			event.Detail = current.Error
		}
		events = append(events, event)
	}
	for id, current := range jobs {
		previous, exists := s.jobs[id]
		if exists && previous.State == current.State {
			continue
		}
		from := ""
		if exists {
			from = previous.State
		}
		if current.Started == nil && activeJob(current.State) {
			started := now
			current.Started = &started
		}
		if current.Ended == nil && current.State != "queued" && !activeJob(current.State) && exists {
			ended := now
			current.Ended = &ended
			if current.Started != nil {
				duration := ended.Sub(*current.Started).Milliseconds()
				current.DurationMS = &duration
			}
		}
		jobs[id] = current
		add(id, previous, current, from, current.State)
	}
	for id, previous := range s.jobs {
		if _, kept := jobs[id]; !kept {
			add(id, previous, jobRecord{}, previous.State, "deleted")
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].JobID < events[j].JobID })
	for i := range events {
		events[i].Seq = s.history.seq + int64(i) + 1
	}
	return events
}

// appendEvents durably appends uncommitted events and returns the previous
// log size for rollback. A partial append that cannot be undone stops all
// further storage work.
func (s *Server) appendEvents(events []JobEvent) (int64, error) {
	h := &s.history
	var buf bytes.Buffer
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			return h.size, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	previous := h.size
	if h.file == nil {
		return previous, errors.New("job event history is not open")
	}
	_, err := h.file.WriteAt(buf.Bytes(), previous)
	if err == nil {
		err = h.file.Sync()
	}
	if err != nil {
		s.rollbackEvents(previous)
		return previous, err
	}
	h.size = previous + int64(buf.Len())
	return previous, nil
}

// rollbackEvents removes events appended for a save whose jobs.json commit
// failed, so their sequence numbers are never reused for different events.
func (s *Server) rollbackEvents(size int64) {
	h := &s.history
	if err := h.file.Truncate(size); err != nil {
		s.failStorage(fmt.Errorf("roll back job events: %w", err))
		return
	}
	if err := h.file.Sync(); err != nil {
		s.failStorage(fmt.Errorf("roll back job events: %w", err))
		return
	}
	h.size = size
}

// commitEvents publishes events whose jobs.json commit succeeded, logs one
// record per event, applies retention, and compacts the log once enough
// events were pruned. previous is the library before this commit, which still
// names deleted jobs.
func (s *Server) commitEvents(events []JobEvent, previous map[string]jobRecord, now time.Time) {
	h := &s.history
	for _, event := range events {
		h.events = append(h.events, event)
		h.seq = event.Seq
		h.inFile++
		job, exists := s.jobs[event.JobID]
		if !exists {
			job = previous[event.JobID]
		}
		s.logJobTransition(event, job.Job)
	}
	h.prune(now)
	if pruned := h.inFile - len(h.events); pruned > max(16, h.limit/10) {
		if err := s.rewriteHistory(h.events, h.seq); err != nil {
			s.logger.Error("compact job event history", "error", err)
		}
	}
}

// prune applies the count and age retention to the in-memory history.
func (h *eventHistory) prune(now time.Time) {
	drop := 0
	if len(h.events) > h.limit {
		drop = len(h.events) - h.limit
	}
	if h.retention > 0 {
		cutoff := now.Add(-h.retention)
		for drop < len(h.events) && h.events[drop].Time.Before(cutoff) {
			drop++
		}
	}
	if drop > 0 {
		h.events = append([]JobEvent(nil), h.events[drop:]...)
	}
}

func (h *eventHistory) close() error {
	if h.file == nil {
		return nil
	}
	err := h.file.Close()
	h.file = nil
	return err
}

// listEvents returns one job's retained history, oldest first. History stays
// readable after the job is deleted until retention removes it.
func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.expire(); err != nil {
		fail(w, 500, "could not persist driver expiration")
		return
	}
	var cutoff time.Time
	if s.history.retention > 0 {
		cutoff = time.Now().UTC().Add(-s.history.retention)
	}
	events := []JobEvent{}
	for _, event := range s.history.events {
		if event.JobID == id && !event.Time.Before(cutoff) {
			events = append(events, event)
		}
	}
	if _, exists := s.jobs[id]; !exists && len(events) == 0 {
		fail(w, 404, "job not found")
		return
	}
	respond(w, 200, map[string]any{"job_id": id, "events": events})
}

func defaultLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.New(slog.DiscardHandler)
}
