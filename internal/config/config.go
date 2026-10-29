// Package config loads and saves the saved connections file. Passwords are
// never written here; they live in the OS keychain (see package secret).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Connection describes one PostgreSQL instance the user can connect to.
type Connection struct {
	Name       string `yaml:"name"`
	Host       string `yaml:"host"`
	Port       int    `yaml:"port"`
	User       string `yaml:"user"`
	Database   string `yaml:"database"`
	SSLMode    string `yaml:"sslmode,omitempty"`
	ReadOnly   bool   `yaml:"read_only,omitempty"`
	Production bool   `yaml:"production,omitempty"`
}

// Validate checks the fields required to open a connection.
func (c Connection) Validate() error {
	switch {
	case strings.TrimSpace(c.Name) == "":
		return errors.New("name is required")
	case strings.TrimSpace(c.Host) == "":
		return errors.New("host is required")
	case c.Port <= 0 || c.Port > 65535:
		return fmt.Errorf("invalid port %d", c.Port)
	case strings.TrimSpace(c.User) == "":
		return errors.New("user is required")
	}
	return nil
}

// DefaultDatabase is the database used to bootstrap the session.
func (c Connection) DefaultDatabase() string {
	if c.Database != "" {
		return c.Database
	}
	return "postgres"
}

// Store is the in-memory view of the connections file.
type Store struct {
	path        string
	Connections []Connection `yaml:"connections"`
}

// DefaultPath returns <user config dir>/dbui/connections.yaml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dbui", "connections.yaml"), nil
}

// Load reads the store from path. A missing file yields an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Path returns the file backing the store.
func (s *Store) Path() string { return s.path }

// Get returns the connection with the given name.
func (s *Store) Get(name string) (Connection, bool) {
	for _, c := range s.Connections {
		if c.Name == name {
			return c, true
		}
	}
	return Connection{}, false
}

// Upsert adds c, or replaces the connection named oldName (which may differ
// from c.Name when the connection is being renamed). It does not save.
func (s *Store) Upsert(oldName string, c Connection) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Name != oldName {
		if _, exists := s.Get(c.Name); exists {
			return fmt.Errorf("a connection named %q already exists", c.Name)
		}
	}
	for i := range s.Connections {
		if s.Connections[i].Name == oldName {
			s.Connections[i] = c
			return nil
		}
	}
	s.Connections = append(s.Connections, c)
	return nil
}

// Delete removes the connection with the given name. It does not save.
func (s *Store) Delete(name string) {
	out := s.Connections[:0]
	for _, c := range s.Connections {
		if c.Name != name {
			out = append(out, c)
		}
	}
	s.Connections = out
}

// Save writes the store atomically with 0600 permissions.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".connections-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
