// Package secret stores connection passwords in the OS keychain.
package secret

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "dbui"

// ErrNotFound is returned when no password is stored for a connection.
var ErrNotFound = errors.New("secret not found")

// Store reads and writes passwords keyed by connection name.
type Store interface {
	Get(name string) (string, error)
	Set(name, password string) error
	Delete(name string) error
}

// Keyring is the OS keychain backed Store.
type Keyring struct{}

func (Keyring) Get(name string) (string, error) {
	pw, err := keyring.Get(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return pw, err
}

func (Keyring) Set(name, password string) error {
	return keyring.Set(service, name, password)
}

func (Keyring) Delete(name string) error {
	err := keyring.Delete(service, name)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
