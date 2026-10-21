package addon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Approval is what the owner approved on this computer: the exact bytes of one version of one add-on.
type Approval struct {
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	PackageHash  string    `json:"package_hash"`
	ManifestHash string    `json:"manifest_hash"`
	By           string    `json:"by"`
	At           time.Time `json:"at"`
}

// ApprovalsFile is kept next to the add-on folders. It is not a valid add-on name, so it cannot clash.
const ApprovalsFile = "approvals.json"

// Store is the approvals for one computer, in a file inside the link's Handloom home.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore keeps approvals in <root>/approvals.json, where root is the folder that holds the add-ons.
func NewStore(root string) *Store { return &Store{path: filepath.Join(root, ApprovalsFile)} }

func (s *Store) read() (map[string]Approval, error) {
	m := map[string]Approval{}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("the approvals file %s is damaged: %v", s.path, err)
	}
	return m, nil
}

// write replaces the file in one step: a temporary file in the same folder, then a rename, so a
// crash leaves the old file or the new one and never half of one.
func (s *Store) write(m map[string]Approval) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".approvals-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// Get returns the approval for name.
func (s *Store) Get(name string) (Approval, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	a, ok := m[name]
	return a, ok, err
}

// Put records an approval, replacing any earlier one for the same name.
func (s *Store) Put(a Approval) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	m[a.Name] = a
	return s.write(m)
}

// Delete forgets the approval for name. Forgetting one that is not there is not an error.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := m[name]; !ok {
		return nil
	}
	delete(m, name)
	return s.write(m)
}

// List returns every approval, by name.
func (s *Store) List() ([]Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]Approval, 0, len(m))
	for _, a := range m {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
