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

	// The seed defines which code prefix each category uses (SYSTEM ->
	// SYS, NETWORK -> NET, ...). Unknown events are allocated under the
	// same prefix so one category never ends up with two numbering
	// sequences (SYS-001 vs SYSTEM-001).
	catToCode map[string]string
	codeToCat map[string]string
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
		entries:   make(map[string]Entry),
		maxNum:    make(map[string]int),
		catToCode: make(map[string]string),
		codeToCat: make(map[string]string),
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
		if prev, ok := r.catToCode[entry.Category]; ok && prev != entry.Code {
			return nil, fmt.Errorf("registry %s: category %s uses two code prefixes (%s and %s)", path, entry.Category, prev, entry.Code)
		}
		r.catToCode[entry.Category] = entry.Code
		r.codeToCat[entry.Code] = entry.Category
	}

	return r, nil
}

// ResolveCategory maps a client-supplied category hint to a known
// (category, code) pair. The hint may be either the category name
// ("SYSTEM") or its code prefix ("SYS"), case-insensitive. ok is false
// for anything the seed doesn't define, so callers can fall back instead
// of minting an arbitrary new prefix from client input.
func (r *Registry) ResolveCategory(hint string) (category, code string, ok bool) {
	hint = strings.ToUpper(strings.TrimSpace(hint))
	r.mu.Lock()
	defer r.mu.Unlock()
	if code, ok := r.catToCode[hint]; ok {
		return hint, code, true
	}
	if cat, ok := r.codeToCat[hint]; ok {
		return cat, hint, true
	}
	return "", "", false
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

// ReservedFrom starts the number range kept for events the SDK emits
// itself (registry.yaml). Allocation ignores it, so learned events keep
// counting up from the regular range (SYS-002, not SYS-902).
const ReservedFrom = 900

func (r *Registry) register(e Entry) {
	r.entries[e.EventName] = e
	if e.Number < ReservedFrom && e.Number > r.maxNum[e.Code] {
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

	// Two concurrent first-sight requests for the same event both miss
	// Lookup; the second must get the first's number, not a fresh one
	// (the store keeps only the first, so a fresh one would change the
	// event's code after a restart).
	if e, ok := r.entries[eventName]; ok {
		return e
	}

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
