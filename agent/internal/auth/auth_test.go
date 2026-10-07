package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAddUserAndVerify(t *testing.T) {
	p := filepath.Join(t.TempDir(), "users.yaml")
	if err := AddUser(p, "indra", RoleAdmin, "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if err := AddUser(p, "noc", RoleViewer, "another long password"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(p); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("users.yaml mode = %v, want 0600", fi.Mode().Perm())
	}
	u, err := LoadUsers(p)
	if err != nil {
		t.Fatal(err)
	}
	if u.Count() != 2 {
		t.Fatalf("count = %d, want 2", u.Count())
	}
	if usr, ok := u.Verify("indra", "correct horse battery"); !ok || usr.Role != RoleAdmin {
		t.Fatalf("Verify(indra) = %v, %v", usr, ok)
	}
	if _, ok := u.Verify("indra", "wrong password!!"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := u.Verify("ghost", "correct horse battery"); ok {
		t.Fatal("unknown user accepted")
	}
}

func TestAddUserRejectsBadInput(t *testing.T) {
	p := filepath.Join(t.TempDir(), "users.yaml")
	if AddUser(p, "bad name", RoleAdmin, "long enough password") == nil {
		t.Error("accepted username with space")
	}
	if AddUser(p, "x", "root", "long enough password") == nil {
		t.Error("accepted invalid role")
	}
	if AddUser(p, "x", RoleViewer, "short") == nil {
		t.Error("accepted short password")
	}
}

func TestLoadUsersMissingFileIsEmpty(t *testing.T) {
	u, err := LoadUsers(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil || u.Count() != 0 {
		t.Fatalf("got %v users, err %v", u.Count(), err)
	}
}

func TestSessions(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	s, _ := NewSessions("secret-a", time.Hour)
	tok := s.Issue("indra", now)

	if u, ok := s.Verify(tok, now.Add(59*time.Minute)); !ok || u != "indra" {
		t.Fatalf("valid token rejected: %q %v", u, ok)
	}
	if _, ok := s.Verify(tok, now.Add(time.Hour)); ok {
		t.Error("expired token accepted")
	}
	other, _ := NewSessions("secret-b", time.Hour)
	if _, ok := other.Verify(tok, now); ok {
		t.Error("token signed with another secret accepted")
	}
	// Flip one character of the payload: signature must no longer match.
	tampered := []byte(tok)
	tampered[2] ^= 1
	if _, ok := s.Verify(string(tampered), now); ok {
		t.Error("tampered token accepted")
	}
	if _, ok := s.Verify("garbage", now); ok {
		t.Error("garbage accepted")
	}
}
