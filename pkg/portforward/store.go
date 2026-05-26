package portforward

import (
	"sort"
	"sync"
)

// Mode controls how a port is exposed.
type Mode string

const (
	// ModeProxy routes traffic through guppi's HTTP reverse-proxy at
	// /proxy/{port}/ — HTTP/WebSocket only, inherits guppi auth.
	ModeProxy Mode = "proxy"
	// ModeSocat binds the port on 0.0.0.0 via socat, exposing raw TCP
	// directly. Works for any protocol; no auth layer.
	ModeSocat Mode = "socat"
)

// Forward is a single port-forwarding entry.
type Forward struct {
	Port         int    `json:"port"`
	Label        string `json:"label"`
	Mode         Mode   `json:"mode"`
	// ExternalPort is the port socat binds on 0.0.0.0 (socat mode only).
	// Must differ from Port because the service already owns Port on 127.0.0.1.
	ExternalPort int    `json:"external_port,omitempty"`

	// socatPID is the PID of the socat child process, or 0 if not running.
	// Not exported — callers use Store methods.
	socatPID int
}

// Store is a thread-safe in-memory registry of active port forwards.
// It owns the socat child-process lifecycle.
type Store struct {
	mu       sync.Mutex
	forwards map[int]*Forward
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{forwards: make(map[int]*Forward)}
}

// Add registers a port forward. If mode is ModeSocat the socat process is
// started immediately; any previously registered forward on that port is
// replaced (stopping the old socat if necessary).
//
// For socat mode, externalPort is the port socat binds on 0.0.0.0. It must
// be different from port because the service already owns port on 127.0.0.1.
// Pass 0 to use the same value as port (only works if nothing else owns it).
func (s *Store) Add(port int, label string, mode Mode, externalPort int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clean up existing entry for this port.
	if existing, ok := s.forwards[port]; ok {
		if existing.socatPID != 0 {
			stopSocat(existing.socatPID) //nolint:errcheck
		}
	}

	if externalPort == 0 {
		externalPort = port
	}

	fwd := &Forward{Port: port, Label: label, Mode: mode, ExternalPort: externalPort}

	if mode == ModeSocat {
		pid, err := startSocat(externalPort, port)
		if err != nil {
			return err
		}
		fwd.socatPID = pid
	}

	s.forwards[port] = fwd
	return nil
}

// Remove stops any associated socat process and deletes the entry.
func (s *Store) Remove(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if fwd, ok := s.forwards[port]; ok {
		if fwd.socatPID != 0 {
			stopSocat(fwd.socatPID) //nolint:errcheck
		}
		delete(s.forwards, port)
	}
}

// List returns all registered forwards sorted by port number.
func (s *Store) List() []Forward {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]Forward, 0, len(s.forwards))
	for _, f := range s.forwards {
		result = append(result, *f)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Port < result[j].Port
	})
	return result
}

// StopAll terminates all running socat processes. Call on server shutdown.
func (s *Store) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, fwd := range s.forwards {
		if fwd.socatPID != 0 {
			stopSocat(fwd.socatPID) //nolint:errcheck
		}
	}
}
