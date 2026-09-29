// Package vault stores provider credentials and generated secrets encrypted
// at rest. On Windows every value is sealed with DPAPI, the same user-bound
// protection Windows Credential Manager uses, so the file is useless on
// another machine or to another Windows account. Other platforms (used for
// development and tests) use AES-256-GCM with a 0600 key file.
//
// Nothing outside this package ever writes a secret to disk.
package vault

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned when a key has no stored value.
var ErrNotFound = errors.New("vault: no such secret")

type entry struct {
	Sealed  string    `json:"sealed"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	Label   string    `json:"label,omitempty"`
}

type fileFormat struct {
	Version int              `json:"version"`
	Scheme  string           `json:"scheme"`
	Entries map[string]entry `json:"entries"`
}

// Vault is an encrypted key/value store for secrets.
type Vault struct {
	mu      sync.Mutex
	path    string
	sealer  sealer
	entries map[string]entry
	redact  *Redactor
}

// sealer is implemented per platform.
type sealer interface {
	Scheme() string
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// Open loads (or creates) the vault in dir.
func Open(dir string, r *Redactor) (*Vault, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s, err := newSealer(dir)
	if err != nil {
		return nil, err
	}
	v := &Vault{path: filepath.Join(dir, "vault.dat"), sealer: s, entries: map[string]entry{}, redact: r}
	bs, err := os.ReadFile(v.path)
	if err == nil {
		var f fileFormat
		if err := json.Unmarshal(bs, &f); err != nil {
			return nil, fmt.Errorf("vault file is damaged: %w", err)
		}
		if f.Scheme != "" && f.Scheme != s.Scheme() {
			return nil, fmt.Errorf("vault was sealed with %s but this system uses %s", f.Scheme, s.Scheme())
		}
		if f.Entries != nil {
			v.entries = f.Entries
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	// Teach the redactor every stored secret so logs can never echo them.
	if r != nil {
		for k := range v.entries {
			if plain, err := v.getLocked(k); err == nil {
				r.Add(string(plain))
			}
		}
	}
	return v, nil
}

// Scheme names the protection in use (shown on the Security screen).
func (v *Vault) Scheme() string { return v.sealer.Scheme() }

// Put stores or replaces a secret.
func (v *Vault) Put(key string, value []byte, label string) error {
	if key == "" {
		return errors.New("vault: empty key")
	}
	sealed, err := v.sealer.Seal(value)
	if err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	now := time.Now().UTC()
	e := v.entries[key]
	if e.Created.IsZero() {
		e.Created = now
	}
	e.Updated = now
	e.Sealed = base64.StdEncoding.EncodeToString(sealed)
	if label != "" {
		e.Label = label
	}
	v.entries[key] = e
	if v.redact != nil {
		v.redact.Add(string(value))
	}
	return v.saveLocked()
}

// PutString is a convenience wrapper.
func (v *Vault) PutString(key, value, label string) error { return v.Put(key, []byte(value), label) }

// Get returns a secret.
func (v *Vault) Get(key string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.getLocked(key)
}

// GetString returns a secret as a string.
func (v *Vault) GetString(key string) (string, error) {
	b, err := v.Get(key)
	return string(b), err
}

func (v *Vault) getLocked(key string) ([]byte, error) {
	e, ok := v.entries[key]
	if !ok {
		return nil, ErrNotFound
	}
	raw, err := base64.StdEncoding.DecodeString(e.Sealed)
	if err != nil {
		return nil, err
	}
	return v.sealer.Open(raw)
}

// Has reports whether a key exists.
func (v *Vault) Has(key string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.entries[key]
	return ok
}

// Delete removes a secret.
func (v *Vault) Delete(key string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.entries[key]; !ok {
		return nil
	}
	delete(v.entries, key)
	return v.saveLocked()
}

// DeletePrefix removes every secret under a prefix (a project, a connection).
func (v *Vault) DeletePrefix(prefix string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	changed := false
	for k := range v.entries {
		if strings.HasPrefix(k, prefix) {
			delete(v.entries, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return v.saveLocked()
}

// Meta describes a stored secret without revealing it.
type Meta struct {
	Key     string    `json:"key"`
	Label   string    `json:"label"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// List returns metadata for every secret (never values).
func (v *Vault) List(prefix string) []Meta {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []Meta
	for k, e := range v.entries {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			out = append(out, Meta{Key: k, Label: e.Label, Created: e.Created, Updated: e.Updated})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (v *Vault) saveLocked() error {
	f := fileFormat{Version: 1, Scheme: v.sealer.Scheme(), Entries: v.entries}
	bs, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := v.path + ".tmp"
	if err := os.WriteFile(tmp, bs, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, v.path)
}
