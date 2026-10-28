package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// SetLogger directs request, job-transition, and storage-failure records to
// logger. A server without one discards them. Call it before serving requests.
func (s *Server) SetLogger(logger *slog.Logger) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s.logger = logger
}

// logRequest records one API or console request. Successful reads and worker
// polls are debug-level because consoles and workers repeat them every few
// seconds; mutations are info, client errors warn, and server errors error.
// The query string and headers are never logged because they can hold secrets.
func (s *Server) logRequest(r *http.Request, status int, elapsed time.Duration) {
	level := slog.LevelInfo
	switch {
	case status >= 500:
		level = slog.LevelError
	case status >= 400:
		level = slog.LevelWarn
	case r.Method == http.MethodGet || r.Method == http.MethodHead || isWorkerPoll(r):
		level = slog.LevelDebug
	}
	s.logger.LogAttrs(context.Background(), level, "http request",
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Int("status", status),
		slog.Duration("duration", elapsed),
		slog.String("remote", r.RemoteAddr),
	)
}

func isWorkerPoll(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/drivers/") && strings.HasSuffix(r.URL.Path, "/poll")
}

// logJobTransition records one committed job event together with the job's
// name and progress. Failed and interrupted jobs are warnings so operators can
// find them at the default level. Session and lease values are logged only as
// the event's opaque references.
func (s *Server) logJobTransition(event JobEvent, job Job) {
	level := slog.LevelInfo
	if event.To == "failed" || event.To == "interrupted" {
		level = slog.LevelWarn
	}
	from := event.From
	if from == "" {
		from = "new"
	}
	attrs := []slog.Attr{
		slog.Int64("seq", event.Seq),
		slog.String("job_id", event.JobID),
		slog.String("name", job.Name),
		slog.String("from", from),
		slog.String("to", event.To),
		slog.String("source", event.Source),
		slog.String("action", event.Action),
	}
	for _, field := range [][2]string{{"credential_id", event.CredentialID}, {"credential_name", event.CredentialName}, {"driver_id", event.DriverID}, {"session_id", event.SessionID}, {"lease_id", event.LeaseID}} {
		if field[1] != "" {
			attrs = append(attrs, slog.String(field[0], field[1]))
		}
	}
	if event.To != "queued" && event.To != "deleted" {
		attrs = append(attrs, slog.Int("acked", job.Acked), slog.Int("total", job.Total))
	}
	if job.Error != "" && (event.To == "failed" || event.To == "interrupted") {
		attrs = append(attrs, slog.String("error", job.Error))
	}
	s.logger.LogAttrs(context.Background(), level, "job state changed", attrs...)
}

// failStorage disables the API after a write whose durability is uncertain.
// Callers hold the server mutex.
func (s *Server) failStorage(err error) {
	s.storageErr = err
	s.logger.Error("storage failure; API disabled until the server is stopped and the data directory inspected", "dir", s.dir, "error", err)
}
