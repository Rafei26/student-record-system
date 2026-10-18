// models.go - domain types, validation rules and the WAM calculation.
//
// Everything in this file is pure logic: no HTTP, no files. That keeps it
// easy to unit test (see app_test.go) and mirrors the "models.go" layer of the
// three-tier design in the preliminary report.
package main

import (
	"fmt"
	"math"
	"net/mail"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Role: Go has no enum keyword, so an enumeration is a named integer type plus
// iota constants. Starting at 1 makes the zero value mean "not set".
// ---------------------------------------------------------------------------

// Role says what a signed-in user is allowed to do.
type Role int

const (
	RoleStudent Role = iota + 1
	RoleAdmin
)

// Valid reports whether r is one of the declared roles. The compiler does NOT
// enforce this: Role(99) compiles happily, so every boundary must call Valid.
func (r Role) Valid() bool { return r == RoleStudent || r == RoleAdmin }

// String makes Role print as text (fmt, slog and html/template all use it).
func (r Role) String() string {
	switch r {
	case RoleStudent:
		return "student"
	case RoleAdmin:
		return "admin"
	}
	return fmt.Sprintf("Role(%d)", int(r))
}

// MarshalText and UnmarshalText let encoding/json store a Role as readable text.
func (r Role) MarshalText() ([]byte, error) {
	if !r.Valid() {
		return nil, fmt.Errorf("cannot encode invalid role %d", int(r))
	}
	return []byte(r.String()), nil
}

func (r *Role) UnmarshalText(b []byte) error {
	switch string(b) {
	case "student":
		*r = RoleStudent
	case "admin":
		*r = RoleAdmin
	default:
		return fmt.Errorf("unknown role %q", b)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Catalogue of courses and units (fixed sample data kept in code).
// ---------------------------------------------------------------------------

// Unit is one teaching unit. Credit points are the weighting used for WAM.
type Unit struct {
	Code         string `json:"code"`
	Title        string `json:"title"`
	CreditPoints int    `json:"creditPoints"`
}

// Course is a degree a student can be enrolled in.
type Course struct {
	Code string
	Name string
}

// Catalogue groups the courses and units the system knows about.
type Catalogue struct {
	Courses []Course
	Units   []Unit
}

// DefaultCatalogue returns the sample catalogue used by the demo.
func DefaultCatalogue() Catalogue {
	return Catalogue{
		Courses: []Course{
			{"BCS", "Bachelor of Computer Science"},
			{"BCY", "Bachelor of Cyber Security"},
			{"BIT", "Bachelor of Information Technology"},
		},
		Units: []Unit{
			{"CSP1150", "Programming Principles", 15},
			{"CSI1101", "Computer Systems", 15},
			{"MAT1104", "Mathematics for Computing", 15},
			{"CSP2348", "Data Structures and Algorithms", 15},
			{"CSI2132", "Databases", 15},
			{"CSI2101", "Networks and Security", 15},
			{"CSP2104", "Web Development", 15},
			{"CSI3207", "Software Engineering", 15},
			{"CSP3341", "Programming Languages and Paradigms", 15},
			{"CSI3105", "Operating Systems", 15},
		},
	}
}

// Unit looks a unit up by code.
func (c Catalogue) Unit(code string) (Unit, bool) {
	i := slices.IndexFunc(c.Units, func(u Unit) bool { return u.Code == code })
	if i < 0 {
		return Unit{}, false
	}
	return c.Units[i], true
}

// Course looks a course up by code.
func (c Catalogue) Course(code string) (Course, bool) {
	i := slices.IndexFunc(c.Courses, func(x Course) bool { return x.Code == code })
	if i < 0 {
		return Course{}, false
	}
	return c.Courses[i], true
}

// ---------------------------------------------------------------------------
// Student records
// ---------------------------------------------------------------------------

// Enrolment links a student to a unit in a given teaching period.
// Mark is a pointer because Go has no optional type: nil means "no result yet".
type Enrolment struct {
	UnitCode string `json:"unitCode"`
	Year     int    `json:"year"`
	Semester int    `json:"semester"`
	Mark     *int   `json:"mark"`
}

// Student holds the personal and academic record of one student.
type Student struct {
	ID          string      `json:"id"`
	FirstName   string      `json:"firstName"`
	LastName    string      `json:"lastName"`
	Email       string      `json:"email"`
	DateOfBirth string      `json:"dateOfBirth"` // YYYY-MM-DD
	Phone       string      `json:"phone,omitempty"`
	CourseCode  string      `json:"courseCode"`
	Enrolments  []Enrolment `json:"enrolments"`
}

// FullName is a convenience method used by the templates and the audit log.
func (s Student) FullName() string { return s.FirstName + " " + s.LastName }

// Clone returns a deep copy. A plain struct copy would share the Enrolments
// slice and every Mark pointer with the original, so edits could leak back
// into the repository's data - a classic Go aliasing trap.
func (s Student) Clone() Student {
	c := s
	c.Enrolments = make([]Enrolment, len(s.Enrolments))
	for i, e := range s.Enrolments {
		if e.Mark != nil {
			m := *e.Mark
			e.Mark = &m
		}
		c.Enrolments[i] = e
	}
	return c
}

// Normalise trims whitespace and fixes the case of identifier-like fields.
func (s *Student) Normalise() {
	s.ID = strings.TrimSpace(s.ID)
	s.FirstName = strings.TrimSpace(s.FirstName)
	s.LastName = strings.TrimSpace(s.LastName)
	s.Email = strings.ToLower(strings.TrimSpace(s.Email))
	s.DateOfBirth = strings.TrimSpace(s.DateOfBirth)
	s.Phone = strings.TrimSpace(s.Phone)
	s.CourseCode = strings.ToUpper(strings.TrimSpace(s.CourseCode))
}

// Grade converts a mark to the usual Australian grade band.
func Grade(mark int) string {
	switch {
	case mark >= 80:
		return "HD"
	case mark >= 70:
		return "D"
	case mark >= 60:
		return "C"
	case mark >= 50:
		return "P"
	default:
		return "N"
	}
}

// WAM is the credit-point-weighted average of all marked enrolments:
//
//	WAM = sum(mark * creditPoints) / sum(creditPoints)
//
// The weighted sum uses integers so no rounding error builds up; only the final
// division uses float64. ok is false when the student has no marked units.
func (s Student) WAM(cat Catalogue) (wam float64, creditPoints int, ok bool) {
	var weighted int
	for _, e := range s.Enrolments {
		if e.Mark == nil {
			continue // unit still in progress
		}
		u, found := cat.Unit(e.UnitCode)
		if !found {
			continue
		}
		weighted += *e.Mark * u.CreditPoints
		creditPoints += u.CreditPoints
	}
	if creditPoints == 0 {
		return 0, 0, false
	}
	return math.Round(float64(weighted)/float64(creditPoints)*100) / 100, creditPoints, true
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// ValidationErrors maps a form field name to a human-readable message.
// Because it has an Error method it satisfies the built-in error interface.
type ValidationErrors map[string]string

func (v ValidationErrors) Error() string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+v[k])
	}
	return strings.Join(parts, "; ")
}

var (
	idPattern    = regexp.MustCompile(`^\d{8}$`)
	phonePattern = regexp.MustCompile(`^\+?[0-9 ]{8,15}$`)
)

func validName(s string) bool {
	n := utf8.RuneCountInString(s)
	if n == 0 || n > 50 {
		return false
	}
	hasLetter := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case r != ' ' && r != '-' && r != '\'':
			return false
		}
	}
	return hasLetter // a name made only of spaces or hyphens is not a name
}

// Validate checks every field and returns ValidationErrors (as an error) or nil.
//
// Go gotcha: the function must return a literal nil. Returning a nil
// ValidationErrors map as an error would produce a NON-nil interface value
// (a "typed nil"), and callers testing err != nil would wrongly see a failure.
func (s Student) Validate(cat Catalogue) error {
	errs := ValidationErrors{}
	if !idPattern.MatchString(s.ID) {
		errs["id"] = "Student ID must be exactly 8 digits."
	}
	if !validName(s.FirstName) {
		errs["firstName"] = "First name is required (letters, spaces, - and ' only; max 50)."
	}
	if !validName(s.LastName) {
		errs["lastName"] = "Last name is required (letters, spaces, - and ' only; max 50)."
	}
	if addr, err := mail.ParseAddress(s.Email); err != nil || addr.Address != s.Email || len(s.Email) > 254 {
		errs["email"] = "Enter a valid email address."
	}
	if t, err := time.Parse("2006-01-02", s.DateOfBirth); err != nil {
		errs["dateOfBirth"] = "Use the format YYYY-MM-DD."
	} else if t.Year() < 1900 || t.After(time.Now().AddDate(-15, 0, 0)) {
		errs["dateOfBirth"] = "Student must be at least 15 years old (born after 1900)."
	}
	if s.Phone != "" && !phonePattern.MatchString(s.Phone) {
		errs["phone"] = "Phone must be 8-15 digits (optional leading +)."
	}
	if _, ok := cat.Course(s.CourseCode); !ok {
		errs["courseCode"] = "Choose a course from the list."
	}
	if msg := checkEnrolments(s.Enrolments, cat); msg != "" {
		errs["enrolments"] = msg
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// checkEnrolments returns the first problem found, or "" when all are valid.
func checkEnrolments(list []Enrolment, cat Catalogue) string {
	seen := make(map[string]bool, len(list))
	maxYear := time.Now().Year() + 1
	for _, e := range list {
		if _, ok := cat.Unit(e.UnitCode); !ok {
			return fmt.Sprintf("Unknown unit %q.", e.UnitCode)
		}
		if e.Year < 2000 || e.Year > maxYear {
			return fmt.Sprintf("Year for %s must be between 2000 and %d.", e.UnitCode, maxYear)
		}
		if e.Semester != 1 && e.Semester != 2 {
			return fmt.Sprintf("Semester for %s must be 1 or 2.", e.UnitCode)
		}
		if e.Mark != nil && (*e.Mark < 0 || *e.Mark > 100) {
			return fmt.Sprintf("Mark for %s must be between 0 and 100.", e.UnitCode)
		}
		key := fmt.Sprintf("%s/%d/%d", e.UnitCode, e.Year, e.Semester)
		if seen[key] {
			return fmt.Sprintf("%s is already enrolled for %d semester %d.", e.UnitCode, e.Year, e.Semester)
		}
		seen[key] = true
	}
	return ""
}
