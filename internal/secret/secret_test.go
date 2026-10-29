package secret

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestKeyring(t *testing.T) {
	keyring.MockInit()
	var s Store = Keyring{}

	if _, err := s.Get("prod"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.Set("prod", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if pw, err := s.Get("prod"); err != nil || pw != "s3cret" {
		t.Fatalf("got %q, %v", pw, err)
	}
	if err := s.Delete("prod"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("prod"); err != nil {
		t.Fatalf("deleting a missing secret should be a no-op, got %v", err)
	}
}
