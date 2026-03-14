// Package secrets provides OS-native keychain storage for API keys and tokens.
// It wraps go-keyring (macOS Keychain, Linux Secret Service, Windows Credential Manager)
// behind a Keyring interface for testability, with a swappable package-level default.
package secrets

import (
	"errors"
	"sync"

	gokeyring "github.com/zalando/go-keyring"
)

const serviceName = "agent-minder"

// ErrNotFound is returned when a secret does not exist in the keyring.
var ErrNotFound = errors.New("secret not found")

// Keyring abstracts OS keychain operations for testability.
type Keyring interface {
	Get(service, key string) (string, error)
	Set(service, key, value string) error
	Delete(service, key string) error
}

// OSKeyring wraps go-keyring calls to the real OS keychain.
type OSKeyring struct{}

func (OSKeyring) Get(service, key string) (string, error) {
	val, err := gokeyring.Get(service, key)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return val, err
}

func (OSKeyring) Set(service, key, value string) error {
	return gokeyring.Set(service, key, value)
}

func (OSKeyring) Delete(service, key string) error {
	err := gokeyring.Delete(service, key)
	if errors.Is(err, gokeyring.ErrNotFound) {
		return ErrNotFound
	}
	return err
}

// MapKeyring is an in-memory keyring for tests.
type MapKeyring struct {
	mu   sync.Mutex
	data map[string]map[string]string // service -> key -> value
}

// NewMapKeyring returns a new in-memory keyring.
func NewMapKeyring() *MapKeyring {
	return &MapKeyring{data: make(map[string]map[string]string)}
}

func (m *MapKeyring) Get(service, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if svc, ok := m.data[service]; ok {
		if val, ok := svc[key]; ok {
			return val, nil
		}
	}
	return "", ErrNotFound
}

func (m *MapKeyring) Set(service, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[service]; !ok {
		m.data[service] = make(map[string]string)
	}
	m.data[service][key] = value
	return nil
}

func (m *MapKeyring) Delete(service, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if svc, ok := m.data[service]; ok {
		delete(svc, key)
	}
	return nil
}

var (
	defaultKeyring Keyring = OSKeyring{}
	mu             sync.RWMutex
)

// SetDefault swaps the package-level keyring (e.g., for tests).
func SetDefault(k Keyring) {
	mu.Lock()
	defer mu.Unlock()
	defaultKeyring = k
}

func getDefault() Keyring {
	mu.RLock()
	defer mu.RUnlock()
	return defaultKeyring
}

// GetSecret retrieves a secret from the keyring.
func GetSecret(key string) (string, error) {
	return getDefault().Get(serviceName, key)
}

// SetSecret stores a secret in the keyring.
func SetSecret(key, value string) error {
	return getDefault().Set(serviceName, key, value)
}

// DeleteSecret removes a secret from the keyring.
func DeleteSecret(key string) error {
	return getDefault().Delete(serviceName, key)
}

// Available probes the keyring with a test write/delete to check if it's usable.
func Available() bool {
	const probeKey = "__agent_minder_probe__"
	kr := getDefault()
	if err := kr.Set(serviceName, probeKey, "probe"); err != nil {
		return false
	}
	_ = kr.Delete(serviceName, probeKey)
	return true
}
