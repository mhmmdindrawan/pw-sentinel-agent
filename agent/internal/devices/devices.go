// Package devices maps an authenticated kiosk/gate machine (device_id) to
// the location/gate/pos it should be attributed to, and the bearer token
// that authenticates it. One agent now serves many kiosks, so this mapping
// — not the agent's own local config — is the source of truth for identity.
//
// The mapping is loaded once at startup from devices.yaml. There is no
// hot-reload: adding a kiosk is a rare, operator-driven event, so a restart
// is an acceptable and much simpler tradeoff than live file-watching.
package devices

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Entry struct {
	DeviceID string
	Location string
	Gate     string
	POS      string
	Token    string
}

type Registry struct {
	byDeviceID map[string]Entry
	byToken    map[string]Entry
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

	r := &Registry{
		byDeviceID: make(map[string]Entry),
		byToken:    make(map[string]Entry),
	}

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

// ByToken looks up a device entry by its bearer token — the primary
// authentication lookup, since the token is the credential a request
// actually proves possession of.
func (r *Registry) ByToken(token string) (Entry, bool) {
	e, ok := r.byToken[token]
	return e, ok
}

// ByDeviceID looks up a device entry by its device_id, without any auth
// implication — used for diagnostics, not for authenticating requests.
func (r *Registry) ByDeviceID(id string) (Entry, bool) {
	e, ok := r.byDeviceID[id]
	return e, ok
}

// Count returns the number of configured devices.
func (r *Registry) Count() int {
	return len(r.byDeviceID)
}

// All returns every configured device, sorted by location, gate, then
// device_id — the order the fleet view lists them in.
func (r *Registry) All() []Entry {
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
