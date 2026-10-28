package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ConstructorFleet/emberdeck/jobtransfer"
)

const (
	workerSessionHeader = "X-EmberDeck-Session"
	workerLeaseHeader   = "X-EmberDeck-Lease"
	contentDigestHeader = "X-EmberDeck-Content-SHA256"
)

func (s *Server) getWorkerContent(w http.ResponseWriter, r *http.Request) {
	driverID, jobID := r.PathValue("id"), r.PathValue("job_id")
	if !driverName.MatchString(driverID) || !validHexID(jobID) {
		fail(w, http.StatusBadRequest, "invalid driver or job ID")
		return
	}
	session, lease := r.Header.Get(workerSessionHeader), r.Header.Get(workerLeaseHeader)
	if session == "" || len(session) > 128 || lease == "" || len(lease) > 128 {
		fail(w, http.StatusBadRequest, "worker session and lease headers are required")
		return
	}

	identity := requestPrincipal(r)
	s.mu.Lock()
	if err := s.expire(); err != nil {
		s.mu.Unlock()
		fail(w, http.StatusInternalServerError, "could not persist driver expiration")
		return
	}
	driver, ok := s.drivers[driverID]
	if !ok || driver.Session != session || driver.CredentialID != identity.ID || !hasCapability(driver, jobtransfer.Capability) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "worker session does not own a chunked transfer")
		return
	}
	record, ok := s.jobs[jobID]
	if !ok || record.DriverID != driverID || record.Lease != lease || !transferableJobState(record.State) {
		s.mu.Unlock()
		fail(w, http.StatusConflict, "stale or invalid job lease")
		return
	}
	driver.LastSeen = time.Now().UTC()
	s.drivers[driverID] = driver
	s.mu.Unlock()

	start, length, err := jobtransfer.ParseRange(r.Header.Get("Range"), record.Size)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", record.Size))
		fail(w, http.StatusRequestedRangeNotSatisfiable, err.Error())
		return
	}
	content, err := s.readProgramRange(record, start, length)
	if errors.Is(err, os.ErrNotExist) {
		fail(w, http.StatusNotFound, "job program not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "could not read stored job program")
		return
	}
	end := start + length - 1
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(length))
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, record.Size))
	w.Header().Set(contentDigestHeader, record.SHA256)
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(content)
}

func transferableJobState(state string) bool {
	switch state {
	case "running", "pausing", "paused", "resuming":
		return true
	default:
		return false
	}
}

func (s *Server) readProgramRange(record jobRecord, start, length int64) ([]byte, error) {
	file, err := os.Open(s.programPath(record.ID))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != record.Size || start < 0 || length <= 0 || start+length > record.Size {
		return nil, errors.New("stored program does not match its manifest")
	}
	content := make([]byte, int(length))
	if _, err := io.ReadFull(io.NewSectionReader(file, start, length), content); err != nil {
		return nil, err
	}
	return content, nil
}
