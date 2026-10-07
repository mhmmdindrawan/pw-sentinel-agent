package registry

import (
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
	e := r.Allocate("DNS_FAILURE", cat, code, "")
	if e.Code != "NET" || e.Number != 3 {
		t.Fatalf("got %s-%03d, want NET-003", e.Code, e.Number)
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
			got[i] = r.Allocate("BARRIER_STUCK", "SYSTEM", "SYS", "")
		}(i)
	}
	wg.Wait()
	for _, e := range got {
		if e.Number != got[0].Number {
			t.Fatalf("same event allocated different numbers: %d and %d", got[0].Number, e.Number)
		}
	}
	if next := r.Allocate("OTHER_EVENT", "SYSTEM", "SYS", ""); next.Number != got[0].Number+1 {
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
