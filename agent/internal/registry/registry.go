// Package registry resolves a symbolic event name (e.g. "PAYMENT_TIMEOUT")
// into a stable (category, code, number, default severity) tuple used to
// build the final error code STI-G03-PAY-002.
//
// It is seeded from a YAML file shipped with the agent, but any event name
// not present in the seed is NOT rejected: the registry allocates the next
// free number for the event's code group on first sight. Callers are
// expected to persist that allocation (see internal/store) and re-register
// it here so restarts don't reassign a different number to the same event.
package registry

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Entry struct {
	EventName string
	Category  string
	Code      string
	Number    int
	Severity  string
}

type seedFile struct {
	Events map[string]struct {
		Category string `yaml:"category"`
		Code     string `yaml:"code"`
		Number   int    `yaml:"number"`
		Severity string `yaml:"severity"`
	} `yaml:"events"`
}

type Registry struct {
	mu      sync.Mutex
	entries map[string]Entry
	maxNum  map[string]int // per code, highest number in use
}

func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read registry %s: %w", path, err)
	}

	var sf seedFile
	if err := yaml.Unmarshal(raw, &sf); err != nil {
		return nil, fmt.Errorf("parse registry %s: %w", path, err)
	}

	r := &Registry{
		entries: make(map[string]Entry),
		maxNum:  make(map[string]int),
	}

	for name, e := range sf.Events {
		entry := Entry{
			EventName: name,
			Category:  strings.ToUpper(e.Category),
			Code:      strings.ToUpper(e.Code),
			Number:    e.Number,
			Severity:  strings.ToUpper(e.Severity),
		}
		if entry.Severity == "" {
			entry.Severity = "MEDIUM"
		}
		r.register(entry)
	}

	return r, nil
}

// Lookup returns the registered entry for an event name, if any.
func (r *Registry) Lookup(eventName string) (Entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[eventName]
	return e, ok
}

// Register adds or overwrites an entry (used both for seed loading and for
// re-hydrating custom allocations persisted in the local database).
func (r *Registry) Register(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.register(e)
}

func (r *Registry) register(e Entry) {
	r.entries[e.EventName] = e
	if e.Number > r.maxNum[e.Code] {
		r.maxNum[e.Code] = e.Number
	}
}

// Allocate resolves an unknown event name to a brand new Entry, assigning
// the next free number within the given code group. The caller is
// responsible for persisting it and then calling Register so the mapping
// survives a restart.
func (r *Registry) Allocate(eventName, category, code, severity string) Entry {
	r.mu.Lock()
	defer r.mu.Unlock()

	code = strings.ToUpper(code)
	category = strings.ToUpper(category)
	if severity == "" {
		severity = "MEDIUM"
	}

	r.maxNum[code]++
	entry := Entry{
		EventName: eventName,
		Category:  category,
		Code:      code,
		Number:    r.maxNum[code],
		Severity:  strings.ToUpper(severity),
	}
	r.entries[eventName] = entry
	return entry
}

// ErrorCode formats the final identifier: LOCATION-GATE-CODE-NUMBER.
func ErrorCode(location, gate string, e Entry) string {
	return fmt.Sprintf("%s-%s-%s-%03d", strings.ToUpper(location), strings.ToUpper(gate), e.Code, e.Number)
}
