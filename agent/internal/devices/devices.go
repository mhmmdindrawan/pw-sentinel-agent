// Package devices maps an authenticated kiosk/gate machine (device_id) to
// the location/gate/pos it should be attributed to, and the bearer token
// that authenticates it. One agent serves many kiosks, so this mapping —
// not the agent's own local config — is the source of truth for identity.
//
// Two kinds of entry live side by side:
//
//   - static ones from devices.yaml, loaded once at startup (no hot-reload:
//     editing the file is a rare operator action and a restart is fine);
//   - enrolled ones, created at runtime by POST /v1/enroll with the site's
//     enroll key and persisted by the store. Their tokens are kept only as
//     SHA-256 hashes, so a copy of the database can't impersonate a kiosk.
package devices

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Entry struct {
	DeviceID string
	Location string
	Gate     string
	POS      string
	Token    string // plaintext for static entries, "" for enrolled ones
	Enrolled bool   // registered at runtime through POST /v1/enroll
}

type Registry struct {
	mu          sync.RWMutex
	byDeviceID  map[string]Entry
	byToken     map[string]Entry // static entries, by plaintext token
	byTokenHash map[string]Entry // enrolled entries, by HashToken(token)
}

type deviceFile struct {
	Devices map[string]struct {
		Location string `yaml:"location"`
		Gate     string `yaml:"gate"`
		POS      string `yaml:"pos"`
		Token    string `yaml:"token"`
	} `yaml:"devices"`
}

func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read devices file %s: %w", path, err)
	}

	var df deviceFile
	if err := yaml.Unmarshal(raw, &df); err != nil {
		return nil, fmt.Errorf("parse devices file %s: %w", path, err)
	}

	r := newRegistry()
	for id, d := range df.Devices {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("devices file %s: found an entry with a blank device_id", path)
		}
		token := strings.TrimSpace(d.Token)
		if token == "" {
			return nil, fmt.Errorf("devices file %s: device %q has no token", path, id)
		}
		if existing, ok := r.byToken[token]; ok {
			return nil, fmt.Errorf("devices file %s: devices %q and %q share the same token", path, existing.DeviceID, id)
		}

		entry := Entry{
			DeviceID: id,
			Location: strings.ToUpper(strings.TrimSpace(d.Location)),
			Gate:     strings.ToUpper(strings.TrimSpace(d.Gate)),
			POS:      strings.TrimSpace(d.POS),
			Token:    token,
		}
		r.byDeviceID[id] = entry
		r.byToken[token] = entry
	}

	return r, nil
}

// Empty returns a registry with no static devices, for agents that rely on
// enrollment alone (devices.yaml absent).
func Empty() *Registry { return newRegistry() }

func newRegistry() *Registry {
	return &Registry{
		byDeviceID:  make(map[string]Entry),
		byToken:     make(map[string]Entry),
		byTokenHash: make(map[string]Entry),
	}
}

// HashToken is how enrolled tokens are stored and looked up.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ByToken looks up a device entry by its bearer token — the primary
// authentication lookup, since the token is the credential a request
// actually proves possession of.
func (r *Registry) ByToken(token string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if e, ok := r.byToken[token]; ok {
		return e, true
	}
	e, ok := r.byTokenHash[HashToken(token)]
	return e, ok
}

// ByDeviceID looks up a device entry by its device_id, without any auth
// implication — used for diagnostics, not for authenticating requests.
func (r *Registry) ByDeviceID(id string) (Entry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.byDeviceID[id]
	return e, ok
}

// PutEnrolled adds or replaces an enrolled device. A device that exists
// statically in devices.yaml cannot be enrolled over. Re-enrolling an
// enrolled device replaces its token hash (the old token stops working).
func (r *Registry) PutEnrolled(e Entry, tokenHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prev, ok := r.byDeviceID[e.DeviceID]; ok && !prev.Enrolled {
		return fmt.Errorf("device %q is managed in devices.yaml", e.DeviceID)
	}
	r.dropEnrolledLocked(e.DeviceID)
	e.Enrolled, e.Token = true, ""
	r.byDeviceID[e.DeviceID] = e
	r.byTokenHash[tokenHash] = e
	return nil
}

// UpdateEnrolled changes gate/pos of an enrolled device in place (the token
// stays). ok is false for unknown or static devices.
func (r *Registry) UpdateEnrolled(deviceID, gate, pos string) (Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byDeviceID[deviceID]
	if !ok || !e.Enrolled {
		return Entry{}, false
	}
	e.Gate, e.POS = gate, pos
	r.byDeviceID[deviceID] = e
	for h, x := range r.byTokenHash {
		if x.DeviceID == deviceID {
			r.byTokenHash[h] = e
		}
	}
	return e, true
}

// RemoveEnrolled revokes an enrolled device; ok is false if it was not one.
func (r *Registry) RemoveEnrolled(deviceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byDeviceID[deviceID]
	if !ok || !e.Enrolled {
		return false
	}
	r.dropEnrolledLocked(deviceID)
	return true
}

func (r *Registry) dropEnrolledLocked(deviceID string) {
	delete(r.byDeviceID, deviceID)
	for h, x := range r.byTokenHash {
		if x.DeviceID == deviceID {
			delete(r.byTokenHash, h)
		}
	}
}

// Count returns the number of configured devices.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byDeviceID)
}

// All returns every configured device, sorted by location, gate, then
// device_id — the order the fleet view lists them in.
func (r *Registry) All() []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Entry, 0, len(r.byDeviceID))
	for _, e := range r.byDeviceID {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Location != b.Location {
			return a.Location < b.Location
		}
		if a.Gate != b.Gate {
			return a.Gate < b.Gate
		}
		return a.DeviceID < b.DeviceID
	})
	return out
}
