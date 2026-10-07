// Package auth handles dashboard logins: a users.yaml of bcrypt-hashed
// passwords and HMAC-signed session cookies. Device tokens (internal/devices)
// are a separate, write-only credential and are never accepted here.
package auth

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

type Role string

const (
	RoleViewer Role = "viewer"
	RoleAdmin  Role = "admin"
)

type User struct {
	Name string
	Role Role
	hash []byte
}

type Users struct {
	byName map[string]User
}

type usersFile struct {
	Users map[string]struct {
		PasswordHash string `yaml:"password_hash"`
		Role         string `yaml:"role"`
	} `yaml:"users"`
}

// dummyHash is compared against when a username is unknown, so a login
// attempt takes the same time whether or not the user exists.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("parkways-sentinel-dummy"), bcrypt.DefaultCost)

// LoadUsers reads users.yaml. A missing file yields an empty set (nobody
// can log in) rather than an error, so an ingest-only agent still starts.
func LoadUsers(path string) (*Users, error) {
	u := &Users{byName: map[string]User{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return u, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read users file %s: %w", path, err)
	}
	var f usersFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse users file %s: %w", path, err)
	}
	for name, e := range f.Users {
		role := Role(strings.ToLower(strings.TrimSpace(e.Role)))
		if role != RoleViewer && role != RoleAdmin {
			return nil, fmt.Errorf("users file %s: user %q has invalid role %q (want viewer or admin)", path, name, e.Role)
		}
		if _, err := bcrypt.Cost([]byte(e.PasswordHash)); err != nil {
			return nil, fmt.Errorf("users file %s: user %q has an invalid password_hash", path, name)
		}
		u.byName[name] = User{Name: name, Role: role, hash: []byte(e.PasswordHash)}
	}
	return u, nil
}

func (u *Users) Count() int { return len(u.byName) }

// Lookup returns a user by name without checking a password — used to
// re-validate a session against the current users.yaml.
func (u *Users) Lookup(name string) (User, bool) {
	usr, ok := u.byName[name]
	return usr, ok
}

// Verify checks a username/password pair.
func (u *Users) Verify(name, password string) (User, bool) {
	usr, ok := u.byName[name]
	hash := dummyHash
	if ok {
		hash = usr.hash
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || !ok {
		return User{}, false
	}
	return usr, true
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,64}$`)

// AddUser creates or replaces a user in users.yaml (mode 0600).
func AddUser(path, name string, role Role, password string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid username %q (letters, digits, . _ - only)", name)
	}
	if role != RoleViewer && role != RoleAdmin {
		return fmt.Errorf("invalid role %q (want viewer or admin)", role)
	}
	if len(password) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	var f usersFile
	if raw, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return fmt.Errorf("parse users file %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if f.Users == nil {
		f.Users = map[string]struct {
			PasswordHash string `yaml:"password_hash"`
			Role         string `yaml:"role"`
		}{}
	}
	f.Users[name] = struct {
		PasswordHash string `yaml:"password_hash"`
		Role         string `yaml:"role"`
	}{string(hash), string(role)}

	out, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	header := "# Parkways Sentinel dashboard users — managed by `sentinel-agent user add`.\n"
	return os.WriteFile(path, append([]byte(header), out...), 0o600)
}

// Names lists usernames and roles, for `sentinel-agent user list`.
func (u *Users) Names() []User {
	out := make([]User, 0, len(u.byName))
	for _, usr := range u.byName {
		out = append(out, usr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
