package devices

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	validDeviceID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	validLabel    = regexp.MustCompile(`^[A-Z0-9]{1,16}$`)
)

// AddDevice appends a new device with a freshly generated token to
// devices.yaml and returns the token. It appends text rather than
// re-marshalling, so operator comments in the file survive. The result is
// re-parsed before it replaces the original, and the file stays 0600.
func AddDevice(path, deviceID, location, gate, pos string) (string, error) {
	location = strings.ToUpper(strings.TrimSpace(location))
	gate = strings.ToUpper(strings.TrimSpace(gate))
	pos = strings.TrimSpace(pos)
	if !validDeviceID.MatchString(deviceID) {
		return "", fmt.Errorf("invalid device id %q (letters, digits, . _ - only)", deviceID)
	}
	if !validLabel.MatchString(location) || !validLabel.MatchString(gate) {
		return "", fmt.Errorf("location and gate must be short codes like STI and G03")
	}
	if pos != "" && !validDeviceID.MatchString(pos) {
		return "", fmt.Errorf("invalid pos %q", pos)
	}

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	before := 0
	if len(existing) > 0 {
		reg, err := Load(path)
		if err != nil {
			return "", err
		}
		if _, dup := reg.ByDeviceID(deviceID); dup {
			return "", fmt.Errorf("device %q already exists in %s", deviceID, path)
		}
		before = reg.Count()
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)

	content := string(existing)
	if before == 0 && !strings.Contains(content, "devices:") {
		content += "devices:\n"
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += fmt.Sprintf("\n  %s:\n    location: %s\n    gate: %s\n", deviceID, location, gate)
	if pos != "" {
		content += fmt.Sprintf("    pos: %s\n", pos)
	}
	content += fmt.Sprintf("    token: \"%s\"\n", token)

	tmp, err := os.CreateTemp(filepath.Dir(path), ".devices-*.yaml")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return "", err
	}
	reg, err := Load(tmp.Name())
	if err != nil {
		return "", fmt.Errorf("refusing to write: result would not parse: %w", err)
	}
	if reg.Count() != before+1 {
		return "", fmt.Errorf("refusing to write: expected %d devices after adding, got %d (is %s hand-edited oddly?)", before+1, reg.Count(), path)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return token, nil
}
