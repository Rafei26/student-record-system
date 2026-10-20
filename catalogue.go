// catalogue.go - the catalogue of units, which administrators can change.
//
// Courses are fixed sample data, but units can be created, edited and removed
// at run time and are saved to data/units.json.
//
// Lock order (to avoid deadlock): code that holds the repository lock may call
// CatalogueStore.Current(), so the CatalogueStore must never call the repository
// while it holds its own lock. Server.deleteUnit follows this rule.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var (
	ErrUnitNotFound = errors.New("unit not found")
	ErrUnitExists   = errors.New("unit already exists")
)

// CatalogueSource is anything that can supply the current catalogue. The
// repository depends on this small interface, not on a concrete type.
type CatalogueSource interface {
	Current() Catalogue
}

// Current lets a fixed Catalogue value act as its own CatalogueSource. The unit
// tests use this to pass a plain value where a live store is used in production.
func (c Catalogue) Current() Catalogue { return c }

// CreditPointOptions are the credit-point values a unit may have.
var CreditPointOptions = []int{5, 10, 15, 20, 25, 30}

var unitCodePattern = regexp.MustCompile(`^[A-Z]{3}\d{4}$`)

func validTitle(s string) bool {
	n := utf8.RuneCountInString(s)
	if n < 3 || n > 80 || strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -&'/,:.()+", r) {
			return false
		}
	}
	return true
}

// Normalise trims the title and upper-cases the code.
func (u *Unit) Normalise() {
	u.Code = strings.ToUpper(strings.TrimSpace(u.Code))
	u.Title = strings.TrimSpace(u.Title)
}

// Validate checks one unit; problems are returned as ValidationErrors.
func (u Unit) Validate() error {
	errs := ValidationErrors{}
	if !unitCodePattern.MatchString(u.Code) {
		errs["code"] = "Unit code must be three letters then four digits, e.g. CSP4401."
	}
	if !validTitle(u.Title) {
		errs["title"] = "Title must be 3-80 characters (letters, digits, spaces and - & ' / , : . ( ) +)."
	}
	if !slices.Contains(CreditPointOptions, u.CreditPoints) {
		errs["creditPoints"] = "Credit points must be 5, 10, 15, 20, 25 or 30."
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// CatalogueStore holds the courses (fixed) and units (editable).
type CatalogueStore struct {
	path    string
	mu      sync.RWMutex
	courses []Course
	units   []Unit
}

// NewCatalogueStore loads units from path if the file exists, otherwise it
// starts from the defaults. An empty path keeps everything in memory.
func NewCatalogueStore(path string, defaults Catalogue) (*CatalogueStore, error) {
	st := &CatalogueStore{path: path, courses: slices.Clone(defaults.Courses), units: slices.Clone(defaults.Units)}
	if path == "" {
		return st, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var list []Unit
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, u := range list {
		if err := u.Validate(); err != nil {
			return nil, fmt.Errorf("invalid unit %q in %s: %w", u.Code, path, err)
		}
	}
	st.units = list
	return st, nil
}

// Current returns a snapshot copy, so callers can never change the store by
// editing the slices they receive.
func (st *CatalogueStore) Current() Catalogue {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return Catalogue{Courses: slices.Clone(st.courses), Units: slices.Clone(st.units)}
}

// AddUnit validates and adds a unit.
func (st *CatalogueStore) AddUnit(u Unit) error {
	u.Normalise()
	if err := u.Validate(); err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if slices.ContainsFunc(st.units, func(x Unit) bool { return x.Code == u.Code }) {
		return fmt.Errorf("%w: %s", ErrUnitExists, u.Code)
	}
	next := append(slices.Clone(st.units), u)
	slices.SortFunc(next, func(a, b Unit) int { return strings.Compare(a.Code, b.Code) })
	return st.commit(next)
}

// UpdateUnit changes the title and credit points of an existing unit.
func (st *CatalogueStore) UpdateUnit(code, title string, creditPoints int) error {
	u := Unit{Code: code, Title: title, CreditPoints: creditPoints}
	u.Normalise()
	if err := u.Validate(); err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	i := slices.IndexFunc(st.units, func(x Unit) bool { return x.Code == u.Code })
	if i < 0 {
		return ErrUnitNotFound
	}
	next := slices.Clone(st.units)
	next[i] = u
	return st.commit(next)
}

// RemoveUnit deletes a unit. The caller is responsible for checking that no
// student is enrolled in it (see Server.deleteUnit).
func (st *CatalogueStore) RemoveUnit(code string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	i := slices.IndexFunc(st.units, func(x Unit) bool { return x.Code == code })
	if i < 0 {
		return ErrUnitNotFound
	}
	return st.commit(slices.Delete(slices.Clone(st.units), i, i+1))
}

// commit saves to disk first, then swaps the new list in (same idea as the
// student repository, so memory and file never disagree). Caller holds the lock.
func (st *CatalogueStore) commit(next []Unit) error {
	if st.path != "" {
		if err := writeJSONAtomic(st.path, next); err != nil {
			return err
		}
	}
	st.units = next
	return nil
}
