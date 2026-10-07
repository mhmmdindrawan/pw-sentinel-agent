package devices

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddDeviceToNewFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "devices.yaml")
	tok, err := AddDevice(p, "STI-G01-POS01", "sti", "g01", "POS01")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(tok))
	}
	reg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := reg.ByToken(tok)
	if !ok || e.DeviceID != "STI-G01-POS01" || e.Location != "STI" || e.Gate != "G01" || e.POS != "POS01" {
		t.Fatalf("got %+v, %v", e, ok)
	}
}

func TestAddDeviceKeepsCommentsAndExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "devices.yaml")
	orig := "# operator note: STI gates\ndevices:\n  STI-G03-POS01:\n    location: STI\n    gate: G03\n    token: \"abc\""
	if err := os.WriteFile(p, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AddDevice(p, "STI-G04-POS01", "STI", "G04", ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(raw), "# operator note") {
		t.Error("comment lost")
	}
	reg, err := Load(p)
	if err != nil || reg.Count() != 2 {
		t.Fatalf("count = %d, err %v", reg.Count(), err)
	}
	if _, ok := reg.ByToken("abc"); !ok {
		t.Error("existing device lost")
	}
}

func TestAddDeviceRejectsDuplicateAndBadInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "devices.yaml")
	if _, err := AddDevice(p, "STI-G01-POS01", "STI", "G01", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := AddDevice(p, "STI-G01-POS01", "STI", "G01", ""); err == nil {
		t.Error("duplicate device accepted")
	}
	if _, err := AddDevice(p, "bad id", "STI", "G01", ""); err == nil {
		t.Error("device id with space accepted")
	}
	if _, err := AddDevice(p, "X", "S T I", "G01", ""); err == nil {
		t.Error("bad location accepted")
	}
}
