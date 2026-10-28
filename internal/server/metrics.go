package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var metricJobStates = []string{
	"queued", "running", "pausing", "paused", "resuming", "canceling",
	"completed", "failed", "canceled", "interrupted",
}

type serverMetrics struct {
	registry       *prometheus.Registry
	buildInfo      *prometheus.GaugeVec
	jobs           *prometheus.GaugeVec
	httpRequests   *prometheus.CounterVec
	httpDuration   *prometheus.HistogramVec
	transitions    *prometheus.CounterVec
	driverExpires  prometheus.Counter
	pluginInvokes  *prometheus.CounterVec
	pluginDuration *prometheus.HistogramVec
	versionMu      sync.Mutex
	version        string
	stateCollector *serverStateCollector
}

func newServerMetrics(s *Server) *serverMetrics {
	m := &serverMetrics{
		registry: prometheus.NewRegistry(),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "emberdeck_build_info", Help: "Build information for this EmberDeck server.",
		}, []string{"version"}),
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "emberdeck_http_requests_total", Help: "HTTP requests served by the EmberDeck API and console.",
		}, []string{"method", "code"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "emberdeck_http_request_duration_seconds", Help: "HTTP request duration in seconds for the EmberDeck API and console.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method"}),
		jobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "emberdeck_jobs", Help: "Durable jobs currently observed in each lifecycle state.",
		}, []string{"state"}),
		transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "emberdeck_job_state_transitions_total", Help: "Durable job state transitions observed during this server process.",
		}, []string{"from", "to"}),
		driverExpires: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "emberdeck_driver_expirations_total", Help: "Driver sessions removed after heartbeat expiration.",
		}),
		pluginInvokes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "emberdeck_plugin_invocations_total", Help: "Preflight plugin invocations by bounded outcome.",
		}, []string{"outcome"}),
		pluginDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "emberdeck_plugin_invocation_duration_seconds", Help: "Preflight plugin invocation duration in seconds by bounded outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"outcome"}),
		version: "unknown",
	}
	m.stateCollector = newServerStateCollector(s)
	m.registry.MustRegister(
		m.buildInfo,
		m.httpRequests,
		m.httpDuration,
		m.jobs,
		m.transitions,
		m.driverExpires,
		m.pluginInvokes,
		m.pluginDuration,
		m.stateCollector,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m.buildInfo.WithLabelValues(m.version).Set(1)
	for _, state := range metricJobStates {
		m.jobs.WithLabelValues(state).Set(0)
	}
	return m
}

func (m *serverMetrics) syncJobStates(jobs map[string]Job) {
	counts := make(map[string]float64, len(metricJobStates))
	for _, state := range metricJobStates {
		counts[state] = 0
	}
	for _, job := range jobs {
		if _, ok := counts[job.State]; ok {
			counts[job.State]++
		}
	}
	for state, count := range counts {
		m.jobs.WithLabelValues(state).Set(count)
	}
}

// SetBuildVersion sets the version label exported by emberdeck_build_info.
func (s *Server) SetBuildVersion(version string) {
	if version == "" {
		version = "unknown"
	}
	s.metrics.versionMu.Lock()
	defer s.metrics.versionMu.Unlock()
	if s.metrics.version == version {
		return
	}
	s.metrics.buildInfo.DeleteLabelValues(s.metrics.version)
	s.metrics.version = version
	s.metrics.buildInfo.WithLabelValues(version).Set(1)
}

// MetricsHandler returns an isolated Prometheus scrape handler. When key is
// non-empty, the handler requires Authorization: Bearer <key>.
func (s *Server) MetricsHandler(key string) http.Handler {
	handler := promhttp.HandlerFor(s.metrics.registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+key)) != 1 {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		handler.ServeHTTP(w, r)
	})
}

func (m *serverMetrics) observeHTTP(method string, code int, elapsed time.Duration) {
	method = normalizeMethod(method)
	if code < 100 || code > 599 {
		code = http.StatusInternalServerError
	}
	m.httpRequests.WithLabelValues(method, statusCode(code)).Inc()
	m.httpDuration.WithLabelValues(method).Observe(elapsed.Seconds())
}

func normalizeMethod(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return http.MethodGet
	case http.MethodPost:
		return http.MethodPost
	case http.MethodPut:
		return http.MethodPut
	case http.MethodDelete:
		return http.MethodDelete
	case http.MethodHead:
		return http.MethodHead
	default:
		return "OTHER"
	}
}

func statusCode(code int) string {
	return string([]byte{'0' + byte(code/100), '0' + byte(code/10%10), '0' + byte(code%10)})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

type serverStateCollector struct {
	server                 *Server
	driversOnlineDesc      *prometheus.Desc
	driversBusyDesc        *prometheus.Desc
	oldestHeartbeatAgeDesc *prometheus.Desc
}

func newServerStateCollector(s *Server) *serverStateCollector {
	return &serverStateCollector{
		server:                 s,
		driversOnlineDesc:      prometheus.NewDesc("emberdeck_drivers_online", "Drivers with a heartbeat within the server expiration interval.", nil, nil),
		driversBusyDesc:        prometheus.NewDesc("emberdeck_drivers_busy", "Online drivers currently assigned active work.", nil, nil),
		oldestHeartbeatAgeDesc: prometheus.NewDesc("emberdeck_oldest_driver_heartbeat_age_seconds", "Age in seconds of the oldest heartbeat among online drivers, or zero when none are online.", nil, nil),
	}
}

func (c *serverStateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.driversOnlineDesc
	ch <- c.driversBusyDesc
	ch <- c.oldestHeartbeatAgeDesc
}

func (c *serverStateCollector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now()
	online, busy := 0.0, 0.0
	oldestAge := 0.0
	var drivers []Driver
	c.server.mu.Lock()
	for _, driver := range c.server.drivers {
		drivers = append(drivers, Driver{LastSeen: driver.LastSeen, Busy: driver.Busy})
	}
	c.server.mu.Unlock()
	for _, driver := range drivers {
		age := now.Sub(driver.LastSeen).Seconds()
		if age < 0 {
			age = 0
		}
		if age > driverTTL.Seconds() {
			continue
		}
		online++
		if driver.Busy {
			busy++
		}
		if age > oldestAge {
			oldestAge = age
		}
	}
	ch <- prometheus.MustNewConstMetric(c.driversOnlineDesc, prometheus.GaugeValue, online)
	ch <- prometheus.MustNewConstMetric(c.driversBusyDesc, prometheus.GaugeValue, busy)
	ch <- prometheus.MustNewConstMetric(c.oldestHeartbeatAgeDesc, prometheus.GaugeValue, oldestAge)
}
