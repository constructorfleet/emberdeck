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

// logJobTransition records a durable job state change. Failed and interrupted
// jobs are warnings so operators can find them at the default level.
func (s *Server) logJobTransition(from string, job Job) {
	level := slog.LevelInfo
	if job.State == "failed" || job.State == "interrupted" {
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.String("job_id", job.ID),
		slog.String("name", job.Name),
		slog.String("from", from),
		slog.String("to", job.State),
	}
	if job.DriverID != "" {
		attrs = append(attrs, slog.String("driver_id", job.DriverID))
	}
	if job.State != "queued" {
		attrs = append(attrs, slog.Int("acked", job.Acked), slog.Int("total", job.Total))
	}
	if job.Error != "" {
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
