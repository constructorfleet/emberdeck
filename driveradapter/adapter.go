// Package driveradapter defines the versioned in-process contract for trusted
// controller adapters. The worker/server integration is a separate layer; an
// adapter package must not decide assignment ownership or replay a lease.
package driveradapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

const ProtocolVersion = 1
const MaxProgramBytes = 4 << 20
const MaxConfigBytes = 1 << 20

var (
	ErrUnsupported       = errors.New("driver adapter capability is unsupported")
	ErrAlreadyStarted    = errors.New("driver adapter session already started")
	ErrNotStarted        = errors.New("driver adapter session has not started")
	ErrClosed            = errors.New("driver adapter session is closed")
	ErrInvalidAssignment = errors.New("invalid driver adapter assignment")
	ErrProtocol          = errors.New("driver adapter protocol violation")
)

type Capabilities struct {
	PauseResume      bool `json:"pause_resume"`
	Cancel           bool `json:"cancel"`
	PositionFeedback bool `json:"position_feedback"`
}

type Manifest struct {
	ProtocolVersion int          `json:"protocol_version"`
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	Transport       string       `json:"transport"`
	Capabilities    Capabilities `json:"capabilities"`
}

type Device struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Capabilities Capabilities `json:"capabilities"`
}

// Assignment carries a single server-owned lease and immutable G-code bytes.
// The server remains authoritative for whole-program validation; the adapter
// boundary verifies the size and digest to reject incomplete or altered input.
type Assignment struct {
	ProtocolVersion int    `json:"protocol_version"`
	JobID           string `json:"job_id"`
	LeaseID         string `json:"lease_id"`
	ContentSize     int    `json:"content_size"`
	SHA256          string `json:"sha256"`
	GCode           []byte `json:"gcode"`
}

func NewAssignment(jobID, leaseID string, gcode []byte) Assignment {
	digest := sha256.Sum256(gcode)
	return Assignment{
		ProtocolVersion: ProtocolVersion,
		JobID:           jobID,
		LeaseID:         leaseID,
		ContentSize:     len(gcode),
		SHA256:          hex.EncodeToString(digest[:]),
		GCode:           append([]byte(nil), gcode...),
	}
}

// Adapter discovers concrete devices and configures a session for one device.
// Implementations are trusted code, not isolated processes or sandboxes.
type Adapter interface {
	Manifest() Manifest
	Discover(context.Context) ([]Device, error)
	Configure(context.Context, string, json.RawMessage) (ControllerSession, error)
}

// ControllerSession is the controller-specific lifecycle behind the public
// Session wrapper. Status separates controller-accepted commands from confirmed
// execution feedback; an adapter must not label acceptance as physical motion.
type ControllerSession interface {
	Start(context.Context, Assignment) error
	Status(context.Context) (Status, error)
	Pause(context.Context) error
	Resume(context.Context) error
	Cancel(context.Context) error
	Close() error
}

type State string

const (
	StateIdle       State = "idle"
	StateConnecting State = "connecting"
	StateRunning    State = "running"
	StatePaused     State = "paused"
	StateCancelling State = "cancelling"
	StateCompleted  State = "completed"
	StateFailed     State = "failed"
	StateOffline    State = "offline"
)

type Feedback string

const (
	FeedbackConfirmed   Feedback = "confirmed"
	FeedbackEstimated   Feedback = "estimated"
	FeedbackUnknown     Feedback = "unknown"
	FeedbackUnavailable Feedback = "unavailable"
)

type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z,omitempty"`
}

type Status struct {
	State            State     `json:"state"`
	AcceptedLines    int       `json:"accepted_lines"`
	TotalLines       int       `json:"total_lines"`
	ExecutedLines    *int      `json:"executed_lines,omitempty"`
	ControllerState  string    `json:"controller_state,omitempty"`
	Position         *Position `json:"position,omitempty"`
	PositionFeedback Feedback  `json:"position_feedback"`
}

// Discover validates the adapter's protocol metadata and device catalog before
// returning a stable, sorted view to the host.
func Discover(ctx context.Context, adapter Adapter) ([]Device, error) {
	if ctx == nil || adapter == nil {
		return nil, fmt.Errorf("%w: context and adapter are required", ErrProtocol)
	}
	manifest := adapter.Manifest()
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	devices, err := adapter.Discover(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover %s: %w", manifest.Name, err)
	}
	seen := make(map[string]bool, len(devices))
	for _, device := range devices {
		if device.ID == "" || strings.TrimSpace(device.ID) != device.ID || device.Name == "" {
			return nil, fmt.Errorf("%w: adapter %q returned a device without a valid ID or name", ErrProtocol, manifest.Name)
		}
		if seen[device.ID] {
			return nil, fmt.Errorf("%w: adapter %q returned duplicate device %q", ErrProtocol, manifest.Name, device.ID)
		}
		seen[device.ID] = true
		if !capabilitiesSubset(device.Capabilities, manifest.Capabilities) {
			return nil, fmt.Errorf("%w: device %q advertises undeclared capabilities", ErrProtocol, device.ID)
		}
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })
	return devices, nil
}

// Configure validates discovery and capability metadata before opening one
// concrete device session. The returned wrapper enforces version, lease input,
// one-start-per-session, and capability-gated controls.
func Configure(ctx context.Context, adapter Adapter, deviceID string, config json.RawMessage) (*Session, error) {
	if len(config) > MaxConfigBytes {
		return nil, fmt.Errorf("%w: adapter configuration exceeds %d bytes", ErrProtocol, MaxConfigBytes)
	}
	if len(config) != 0 && !json.Valid(config) {
		return nil, fmt.Errorf("%w: adapter configuration must be one JSON value", ErrProtocol)
	}
	devices, err := Discover(ctx, adapter)
	if err != nil {
		return nil, err
	}
	var device *Device
	for i := range devices {
		if devices[i].ID == deviceID {
			device = &devices[i]
			break
		}
	}
	if device == nil {
		return nil, fmt.Errorf("%w: device %q was not discovered", ErrProtocol, deviceID)
	}
	inner, err := adapter.Configure(ctx, deviceID, append(json.RawMessage(nil), config...))
	if err != nil {
		return nil, fmt.Errorf("configure %s/%s: %w", adapter.Manifest().Name, deviceID, err)
	}
	if inner == nil {
		return nil, fmt.Errorf("%w: adapter returned a nil session", ErrProtocol)
	}
	return &Session{inner: inner, capabilities: device.Capabilities}, nil
}

// Session wraps an adapter-created controller session and applies shared
// lifecycle and capability rules without guessing protocol-specific behavior.
type Session struct {
	mu           sync.Mutex
	inner        ControllerSession
	capabilities Capabilities
	started      bool
	closed       bool
	lastAccepted int
	lastExecuted int
	lastTotal    int
}

func (s *Session) Start(ctx context.Context, assignment Assignment) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrProtocol)
	}
	assignment.GCode = append([]byte(nil), assignment.GCode...)
	if err := validateAssignment(assignment); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.started {
		return ErrAlreadyStarted
	}
	// Treat a failed start as consumed: the controller may have accepted work
	// before a transport error became visible.
	s.started = true
	return s.inner.Start(ctx, assignment)
}

func (s *Session) Status(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Status{}, ErrClosed
	}
	if !s.started {
		return Status{}, ErrNotStarted
	}
	status, err := s.inner.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	if err := s.validateStatus(status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func (s *Session) validateStatus(status Status) error {
	switch status.State {
	case StateIdle, StateConnecting, StateRunning, StatePaused, StateCancelling, StateCompleted, StateFailed, StateOffline:
	default:
		return fmt.Errorf("%w: unknown state %q", ErrProtocol, status.State)
	}
	if status.AcceptedLines < 0 || status.TotalLines < status.AcceptedLines {
		return fmt.Errorf("%w: invalid accepted line count", ErrProtocol)
	}
	if s.lastTotal != 0 && status.TotalLines != s.lastTotal {
		return fmt.Errorf("%w: total line count changed during lease", ErrProtocol)
	}
	s.lastTotal = status.TotalLines
	if status.AcceptedLines < s.lastAccepted {
		return fmt.Errorf("%w: accepted line count regressed", ErrProtocol)
	}
	s.lastAccepted = status.AcceptedLines
	if status.ExecutedLines != nil {
		if *status.ExecutedLines < 0 || *status.ExecutedLines > status.AcceptedLines || *status.ExecutedLines < s.lastExecuted {
			return fmt.Errorf("%w: executed line count is inconsistent or regressed", ErrProtocol)
		}
		s.lastExecuted = *status.ExecutedLines
	}
	if status.Position != nil && !s.capabilities.PositionFeedback {
		return fmt.Errorf("%w: position returned without position-feedback capability", ErrProtocol)
	}
	switch status.PositionFeedback {
	case FeedbackConfirmed, FeedbackEstimated, FeedbackUnknown, FeedbackUnavailable:
	default:
		return fmt.Errorf("%w: invalid position feedback confidence %q", ErrProtocol, status.PositionFeedback)
	}
	if status.Position != nil && (status.PositionFeedback == FeedbackUnavailable) {
		return fmt.Errorf("%w: position is present but marked unavailable", ErrProtocol)
	}
	return nil
}

func (s *Session) Pause(ctx context.Context) error {
	if !s.capabilities.PauseResume {
		return ErrUnsupported
	}
	return s.control(ctx, s.inner.Pause)
}

func (s *Session) Resume(ctx context.Context) error {
	if !s.capabilities.PauseResume {
		return ErrUnsupported
	}
	return s.control(ctx, s.inner.Resume)
}

func (s *Session) Cancel(ctx context.Context) error {
	if !s.capabilities.Cancel {
		return ErrUnsupported
	}
	return s.control(ctx, s.inner.Cancel)
}

func (s *Session) control(ctx context.Context, action func(context.Context) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if !s.started {
		return ErrNotStarted
	}
	return action(ctx)
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.inner.Close()
}

func validateManifest(manifest Manifest) error {
	if manifest.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("%w: unsupported protocol version %d", ErrProtocol, manifest.ProtocolVersion)
	}
	if manifest.Name == "" || manifest.Version == "" || manifest.Transport == "" {
		return fmt.Errorf("%w: adapter name, version, and transport are required", ErrProtocol)
	}
	return nil
}

func capabilitiesSubset(device, manifest Capabilities) bool {
	return (!device.PauseResume || manifest.PauseResume) &&
		(!device.Cancel || manifest.Cancel) &&
		(!device.PositionFeedback || manifest.PositionFeedback)
}

func validateAssignment(assignment Assignment) error {
	if assignment.ProtocolVersion != ProtocolVersion || assignment.JobID == "" || assignment.LeaseID == "" {
		return fmt.Errorf("%w: version, job ID, and lease ID are required", ErrInvalidAssignment)
	}
	if assignment.ContentSize < 0 || assignment.ContentSize > MaxProgramBytes || assignment.ContentSize != len(assignment.GCode) {
		return fmt.Errorf("%w: content size must match bytes and not exceed %d", ErrInvalidAssignment, MaxProgramBytes)
	}
	digest, err := hex.DecodeString(assignment.SHA256)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("%w: SHA-256 must contain 32 bytes", ErrInvalidAssignment)
	}
	actual := sha256.Sum256(assignment.GCode)
	if !equalDigest(digest, actual[:]) {
		return fmt.Errorf("%w: content digest mismatch", ErrInvalidAssignment)
	}
	return nil
}

func equalDigest(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
