// repository.go - persistence layer.
//
// StudentRepository is an interface; JSONRepository (file storage) and
// auditedRepository (a logging decorator) both satisfy it WITHOUT declaring
// that they do - Go interfaces are satisfied implicitly by method sets.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Sentinel errors. Callers test for them with errors.Is, even when wrapped.
var (
	ErrNotFound  = errors.New("student not found")
	ErrDuplicate = errors.New("student already exists")
)

// StudentRepository is everything the web layer needs from storage.
type StudentRepository interface {
	All() ([]Student, error)
	Find(id string) (Student, error)
	Add(s Student) error
	// Modify applies fn to a private copy of the record and saves the result
	// only if the changed record is still valid. fn runs while the repository
	// lock is held, so it must not call back into the repository.
	Modify(id string, fn func(*Student) error) error
	Delete(id string) error
}

// Compile-time proof that the implementations satisfy the interface. This costs
// nothing at run time; the build fails if a method is missing or mistyped.
var (
	_ StudentRepository = (*JSONRepository)(nil)
	_ StudentRepository = auditedRepository{}
)

// JSONRepository keeps all students in memory and saves them to one JSON file
// after every change. An empty path gives a purely in-memory repository, which
// the unit tests use.
type JSONRepository struct {
	path     string
	cats     CatalogueSource // supplies the CURRENT catalogue on every validation
	mu       sync.RWMutex    // many readers OR one writer
	students map[string]Student
}

// NewJSONRepository loads path if it exists and validates every stored record.
func NewJSONRepository(path string, cats CatalogueSource) (*JSONRepository, error) {
	r := &JSONRepository{path: path, cats: cats, students: map[string]Student{}}
	if path == "" {
		return r, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil // first run: start empty
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var list []Student
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cat := cats.Current()
	for _, s := range list {
		if err := s.Validate(cat); err != nil {
			return nil, fmt.Errorf("invalid record %q in %s: %w", s.ID, path, err)
		}
		r.students[s.ID] = s
	}
	return r, nil
}

// All returns copies of every student, sorted by ID.
func (r *JSONRepository) All() ([]Student, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Student, 0, len(r.students))
	for _, s := range r.students {
		out = append(out, s.Clone())
	}
	slices.SortFunc(out, func(a, b Student) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Find returns a copy of one student or ErrNotFound.
func (r *JSONRepository) Find(id string) (Student, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.students[id]
	if !ok {
		return Student{}, ErrNotFound
	}
	return s.Clone(), nil
}

// Add validates and stores a new student.
func (r *JSONRepository) Add(s Student) error {
	s.Normalise()
	if err := s.Validate(r.cats.Current()); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.students[s.ID]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicate, s.ID)
	}
	next := maps.Clone(r.students)
	next[s.ID] = s.Clone()
	return r.commit(next)
}

// Modify edits one student atomically (see the interface comment).
func (r *JSONRepository) Modify(id string, fn func(*Student) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.students[id]
	if !ok {
		return ErrNotFound
	}
	work := cur.Clone()
	if err := fn(&work); err != nil {
		return err
	}
	work.Normalise()
	if work.ID != id {
		return ValidationErrors{"id": "Student ID cannot be changed."}
	}
	if err := work.Validate(r.cats.Current()); err != nil {
		return err
	}
	next := maps.Clone(r.students)
	next[id] = work
	return r.commit(next)
}

// Delete removes a student.
func (r *JSONRepository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.students[id]; !ok {
		return ErrNotFound
	}
	next := maps.Clone(r.students)
	delete(next, id)
	return r.commit(next)
}

// commit writes the new state to disk FIRST and swaps it into memory only if
// the write succeeded, so memory and file can never disagree after an error.
// The caller must hold the write lock.
func (r *JSONRepository) commit(next map[string]Student) error {
	if r.path != "" {
		list := make([]Student, 0, len(next))
		for _, s := range next {
			list = append(list, s)
		}
		slices.SortFunc(list, func(a, b Student) int { return strings.Compare(a.ID, b.ID) })
		if err := writeJSONAtomic(r.path, list); err != nil {
			return err
		}
	}
	r.students = next
	return nil
}

// writeJSONAtomic writes to a temporary file and renames it over the target, so
// a crash mid-write cannot leave a half-written data file behind.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// auditedRepository is a decorator: it embeds ANY StudentRepository, inherits
// all five methods for free, and overrides only the three that change data.
// This is Go's composition-over-inheritance in about ten lines.
type auditedRepository struct {
	StudentRepository
	log *slog.Logger
}

func (a auditedRepository) Add(s Student) error {
	err := a.StudentRepository.Add(s)
	a.log.Info("repository add", "id", s.ID, "error", err)
	return err
}

func (a auditedRepository) Modify(id string, fn func(*Student) error) error {
	err := a.StudentRepository.Modify(id, fn)
	a.log.Info("repository modify", "id", id, "error", err)
	return err
}

func (a auditedRepository) Delete(id string) error {
	err := a.StudentRepository.Delete(id)
	a.log.Info("repository delete", "id", id, "error", err)
	return err
}
