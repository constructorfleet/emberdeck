package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/ConstructorFleet/emberdeck/driveradapter"
)

// AdapterRegistry maps one server driver ID to trusted adapter code and one
// discovered device. Registrations are provided by the embedding process; this
// registry does not load plugins from disk or expose a configuration UI.
type AdapterRegistry struct {
	mu            sync.RWMutex
	registrations map[string]adapterRegistration
}

type adapterRegistration struct {
	adapter      driveradapter.Adapter
	deviceID     string
	config       json.RawMessage
	capabilities driveradapter.Capabilities
}

// NewAdapterRegistry creates an empty in-process adapter registry.
func NewAdapterRegistry() *AdapterRegistry {
	return &AdapterRegistry{registrations: make(map[string]adapterRegistration)}
}

// Register binds a server driver ID to one device from adapter. Adapter
// implementations are trusted code and execute inside this process.
func (r *AdapterRegistry) Register(driverID string, adapter driveradapter.Adapter, deviceID string, config json.RawMessage) error {
	if r == nil || driverID == "" || adapter == nil || deviceID == "" {
		return errors.New("worker: driver ID, adapter, and device ID are required")
	}
	if len(config) > driveradapter.MaxConfigBytes || len(config) > 0 && !json.Valid(config) {
		return errors.New("worker: adapter configuration must be valid JSON within the size limit")
	}
	devices, err := driveradapter.Discover(context.Background(), adapter)
	if err != nil {
		return err
	}
	var capabilities *driveradapter.Capabilities
	for _, device := range devices {
		if device.ID == deviceID {
			caps := device.Capabilities
			capabilities = &caps
			break
		}
	}
	if capabilities == nil {
		return fmt.Errorf("worker: adapter device %q was not discovered", deviceID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.registrations == nil {
		r.registrations = make(map[string]adapterRegistration)
	}
	if _, exists := r.registrations[driverID]; exists {
		return fmt.Errorf("worker: driver %q is already registered", driverID)
	}
	r.registrations[driverID] = adapterRegistration{
		adapter: adapter, deviceID: deviceID,
		config: append(json.RawMessage(nil), config...), capabilities: *capabilities,
	}
	return nil
}

func (r *AdapterRegistry) lookup(driverID string) (adapterRegistration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	registration, ok := r.registrations[driverID]
	registration.config = append(json.RawMessage(nil), registration.config...)
	return registration, ok
}
