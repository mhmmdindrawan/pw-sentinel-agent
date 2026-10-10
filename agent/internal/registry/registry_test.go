package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const seed = `
events:
  APP_STARTED:   {category: APP,     code: APP, number: 1, severity: INFO}
  SYSTEM_ERROR:  {category: SYSTEM,  code: SYS, number: 1, severity: MEDIUM}
  NETWORK_DOWN:  {category: NETWORK, code: NET, number: 1, severity: HIGH}
  HIGH_LATENCY:  {category: NETWORK, code: NET, number: 2, severity: MEDIUM}
`

func loadSeed(t *testing.T, body string) (*Registry, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "registry.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

func TestResolveCategory(t *testing.T) {
	r, err := loadSeed(t, seed)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		hint, cat, code string
		ok              bool
	}{
		{"SYSTEM", "SYSTEM", "SYS", true},
		{"system", "SYSTEM", "SYS", true},
		{" SYS ", "SYSTEM", "SYS", true},
		{"NETWORK", "NETWORK", "NET", true},
		{"NET", "NETWORK", "NET", true},
		{"BOGUS", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		cat, code, ok := r.ResolveCategory(c.hint)
		if cat != c.cat || code != c.code || ok != c.ok {
			t.Errorf("ResolveCategory(%q) = (%q, %q, %v), want (%q, %q, %v)", c.hint, cat, code, ok, c.cat, c.code, c.ok)
		}
	}
}

func TestAllocateContinuesSeedNumbering(t *testing.T) {
	r, err := loadSeed(t, seed)
	if err != nil {
		t.Fatal(err)
	}
	cat, code, _ := r.ResolveCategory("NETWORK")
	e, isNew := r.Allocate("DNS_FAILURE", cat, code, "")
	if e.Code != "NET" || e.Number != 3 || !isNew {
		t.Fatalf("got %s-%03d (new %v), want new NET-003", e.Code, e.Number, isNew)
	}
}

func TestAllocateConcurrentSameEventGetsOneNumber(t *testing.T) {
	r, err := loadSeed(t, seed)
	if err != nil {
		t.Fatal(err)
	}
	const n = 50
	got := make([]Entry, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i], _ = r.Allocate("BARRIER_STUCK", "SYSTEM", "SYS", "")
		}(i)
	}
	wg.Wait()
	for _, e := range got {
		if e.Number != got[0].Number {
			t.Fatalf("same event allocated different numbers: %d and %d", got[0].Number, e.Number)
		}
	}
	if next, _ := r.Allocate("OTHER_EVENT", "SYSTEM", "SYS", ""); next.Number != got[0].Number+1 {
		t.Fatalf("next allocation = %d, want %d (no numbers burned)", next.Number, got[0].Number+1)
	}
}

func TestLoadRejectsCategoryWithTwoPrefixes(t *testing.T) {
	_, err := loadSeed(t, `
events:
  A: {category: SYSTEM, code: SYS, number: 1}
  B: {category: SYSTEM, code: SYSX, number: 1}
`)
	if err == nil {
		t.Fatal("expected error for category mapped to two code prefixes")
	}
}

func TestReservedNumbersDoNotAdvanceAllocation(t *testing.T) {
	r, err := loadSeed(t, seed+"  SENTINEL_GAP: {category: SYSTEM, code: SYS, number: 901, severity: INFO}\n")
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := r.Allocate("FAN_FAILURE", "SYSTEM", "SYS", ""); e.Number != 2 {
		t.Fatalf("got SYS-%03d, want SYS-002", e.Number)
	}
}

func TestAllocateStopsAtOverflowNumber(t *testing.T) {
	r, err := loadSeed(t, seed+"  SENTINEL_GAP: {category: SYSTEM, code: SYS, number: 901, severity: INFO}\n")
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i < OverflowNumber; i++ {
		if e, isNew := r.Allocate(fmt.Sprintf("EV_%d", i), "SYSTEM", "SYS", ""); !isNew || e.Number != i {
			t.Fatalf("allocation %d got SYS-%03d (new %v)", i, e.Number, isNew)
		}
	}
	for _, name := range []string{"ONE_TOO_MANY", "AND_ANOTHER"} {
		e, isNew := r.Allocate(name, "SYSTEM", "SYS", "")
		if isNew || e.Number != OverflowNumber || e.EventName != name {
			t.Fatalf("%s: got %+v (new %v), want shared SYS-%03d, not learned", name, e, isNew, OverflowNumber)
		}
		if _, ok := r.Lookup(name); ok {
			t.Fatalf("%s was learned after the group filled up", name)
		}
	}
	if e, _ := r.Lookup("SENTINEL_GAP"); e.Number != 901 {
		t.Fatalf("reserved SDK event moved to %d", e.Number)
	}
}

func TestSeverityIsNormalized(t *testing.T) {
	r, err := loadSeed(t, seed+"  WARNED: {category: APP, code: APP, number: 2, severity: warning}\n")
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := r.Lookup("WARNED"); e.Severity != "MEDIUM" {
		t.Errorf("seed severity warning -> %q, want MEDIUM", e.Severity)
	}
	if e, _ := r.Allocate("LOG_X__Y", "APP", "APP", "ERROR"); e.Severity != "HIGH" {
		t.Errorf("allocated with ERROR -> %q, want HIGH", e.Severity)
	}
	if e, _ := r.Allocate("LOG_X__Z", "APP", "APP", "whatever"); e.Severity != "MEDIUM" {
		t.Errorf("allocated with unknown severity -> %q, want MEDIUM", e.Severity)
	}
	r.Register(Entry{EventName: "FROM_DB", Category: "APP", Code: "APP", Number: 9, Severity: "WARN"})
	if e, _ := r.Lookup("FROM_DB"); e.Severity != "MEDIUM" {
		t.Errorf("re-registered WARN -> %q, want MEDIUM", e.Severity)
	}
}

func TestLoadRejectsBadSeverityAndNumbers(t *testing.T) {
	for _, line := range []string{
		"  X: {category: APP, code: APP, number: 5, severity: HIHG}\n",
		"  X: {category: APP, code: APP, number: 0, severity: LOW}\n",
		"  X: {category: APP, code: APP, number: 899, severity: LOW}\n",
		"  X: {category: APP, code: APP, number: 1000, severity: LOW}\n",
	} {
		if _, err := loadSeed(t, seed+line); err == nil {
			t.Errorf("seed line %q loaded without error", line)
		}
	}
}

// The registry shipped with the agent must load: a bad severity or number
// there would stop the agent at startup.
func TestShippedRegistryLoads(t *testing.T) {
	r, err := Load(filepath.Join("..", "..", "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := r.Lookup("PAYMENT_TIMEOUT"); !ok || e.Code != "PAY" || e.Number != 2 || e.Severity != "HIGH" {
		t.Errorf("PAYMENT_TIMEOUT = %+v, want PAY-002 HIGH", e)
	}
}
