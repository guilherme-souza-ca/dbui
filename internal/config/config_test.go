package config

import (
	"os"
	"path/filepath"
	"testing"
)

func conn(name string) Connection {
	return Connection{Name: name, Host: "localhost", Port: 5432, User: "postgres"}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Connections) != 0 {
		t.Fatalf("expected empty store, got %v", s.Connections)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "connections.yaml")
	s, _ := Load(path)
	prod := conn("prod")
	prod.Production = true
	prod.SSLMode = "require"
	if err := s.Upsert("", prod); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert("", conn("local")); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := loaded.Get("prod")
	if !ok || got != prod {
		t.Fatalf("got %+v, want %+v", got, prod)
	}
	if len(loaded.Connections) != 2 {
		t.Fatalf("got %d connections", len(loaded.Connections))
	}
}

func TestUpsertRenameAndDuplicates(t *testing.T) {
	s, _ := Load(filepath.Join(t.TempDir(), "c.yaml"))
	_ = s.Upsert("", conn("a"))
	_ = s.Upsert("", conn("b"))

	if err := s.Upsert("", conn("a")); err == nil {
		t.Fatal("expected duplicate name error")
	}
	if err := s.Upsert("a", conn("b")); err == nil {
		t.Fatal("expected error renaming onto an existing name")
	}
	if err := s.Upsert("a", conn("c")); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("a"); ok {
		t.Fatal("old name should be gone after rename")
	}
	if _, ok := s.Get("c"); !ok {
		t.Fatal("renamed connection missing")
	}

	s.Delete("c")
	if len(s.Connections) != 1 || s.Connections[0].Name != "b" {
		t.Fatalf("unexpected connections after delete: %v", s.Connections)
	}
}

func TestValidate(t *testing.T) {
	bad := conn("x")
	bad.Port = 0
	if bad.Validate() == nil {
		t.Fatal("expected invalid port error")
	}
	if (Connection{}).Validate() == nil {
		t.Fatal("expected error for empty connection")
	}
	if conn("x").DefaultDatabase() != "postgres" {
		t.Fatal("default database should be postgres")
	}
}
