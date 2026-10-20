// server.go - HTTP layer: routes, middleware and request handlers.
//
// Go's net/http runs every request in its own goroutine, so the handlers below
// can execute concurrently. All shared state lives behind the repository,
// UserStore and SessionManager, each of which protects itself with a mutex.
package main

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"crypto/subtle"
	"encoding/csv"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"maps"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Server wires the storage, authentication and templates together.
type Server struct {
	repo     StudentRepository
	users    *UserStore
	sessions *SessionManager
	cat      *CatalogueStore
	tmpl     *template.Template
	log      *slog.Logger
}

// NewServer parses the HTML templates once at start-up. template.Must panics on
// a syntax error, so a broken template is caught immediately - but mistakes in
// field names inside templates are only found when a page is rendered.
func NewServer(repo StudentRepository, users *UserStore, sessions *SessionManager, cat *CatalogueStore, log *slog.Logger) *Server {
	return &Server{
		repo: repo, users: users, sessions: sessions, cat: cat, log: log,
		tmpl: template.Must(template.New("app").Parse(allTemplates)),
	}
}

// Routes registers every URL. Patterns such as "GET /students/{id}" (method +
// wildcard) come from the Go 1.22 ServeMux; no third-party router is needed.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	// Public pages: the landing page and one sign-in page per role.
	mux.HandleFunc("GET /{$}", s.root)
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusSeeOther) })
	mux.HandleFunc("GET /login/admin", s.loginForm(RoleAdmin))
	mux.HandleFunc("POST /login/admin", s.loginSubmit(RoleAdmin))
	mux.HandleFunc("GET /login/student", s.loginForm(RoleStudent))
	mux.HandleFunc("POST /login/student", s.loginSubmit(RoleStudent))
	mux.Handle("POST /logout", s.auth(s.logout))

	// Any signed-in user.
	mux.Handle("GET /account/password", s.auth(s.passwordForm))
	mux.Handle("POST /account/password", s.auth(s.passwordSubmit))
	mux.Handle("GET /students/{id}", s.auth(s.showStudent))
	// Students may enrol / unenrol themselves; the handlers check ownership.
	mux.Handle("POST /students/{id}/enrol", s.auth(s.enrol))
	mux.Handle("POST /students/{id}/unenrol", s.auth(s.unenrol))

	// Administrators only.
	mux.Handle("GET /export.csv", s.admin(s.exportCSV))
	mux.Handle("GET /students/new", s.admin(s.newStudentForm))
	mux.Handle("POST /students", s.admin(s.createStudent))
	mux.Handle("GET /students/{id}/edit", s.admin(s.editStudentForm))
	mux.Handle("POST /students/{id}", s.admin(s.updateStudent))
	mux.Handle("POST /students/{id}/delete", s.admin(s.deleteStudent))
	mux.Handle("POST /students/{id}/mark", s.admin(s.setMark))
	mux.Handle("GET /units", s.admin(s.unitsPage))
	mux.Handle("POST /units", s.admin(s.createUnit))
	mux.Handle("POST /units/{code}", s.admin(s.updateUnit))
	mux.Handle("POST /units/{code}/delete", s.admin(s.deleteUnit))

	// Middleware wraps the router: the first in the list is the outermost layer.
	return chain(mux, s.recoverPanic, s.logRequests, securityHeaders)
}

// ---------------------------------------------------------------------------
// Middleware: functions that take a handler and return a handler.
// ---------------------------------------------------------------------------

func chain(h http.Handler, layers ...func(http.Handler) http.Handler) http.Handler {
	for _, l := range slices.Backward(layers) {
		h = l(h)
	}
	return h
}

// recoverPanic turns a panic in any handler into a 500 page instead of letting
// the whole request goroutine die. recover only works inside a deferred call.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler", "value", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				s.fail(w, r, http.StatusInternalServerError, "Something went wrong on our side.")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start).Round(time.Microsecond))
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// sessionFrom finds the session for the request's cookie.
func (s *Server) sessionFrom(r *http.Request) (string, session, bool) {
	c, err := r.Cookie("session")
	if err != nil {
		return "", session{}, false
	}
	sess, ok := s.sessions.Lookup(c.Value)
	return c.Value, sess, ok
}

// auth requires a signed-in user and, for state-changing requests, a valid CSRF token.
func (s *Server) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sess, ok := s.sessionFrom(r)
		if !ok {
			http.Redirect(w, r, "/", http.StatusSeeOther) // back to the sign-in chooser
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if subtle.ConstantTimeCompare([]byte(r.PostFormValue("_csrf")), []byte(sess.CSRF)) != 1 {
				s.fail(w, r, http.StatusForbidden, "Invalid or missing form token. Reload the page and try again.")
				return
			}
		}
		h(w, r)
	})
}

// admin is auth plus a role check: students get 403 Forbidden.
func (s *Server) admin(h http.HandlerFunc) http.Handler {
	return s.auth(func(w http.ResponseWriter, r *http.Request) {
		_, sess, _ := s.sessionFrom(r)
		if sess.User.Role != RoleAdmin {
			s.fail(w, r, http.StatusForbidden, "Only administrators can do that.")
			return
		}
		h(w, r)
	})
}

// ---------------------------------------------------------------------------
// View models (what the templates receive)
// ---------------------------------------------------------------------------

type pageData struct {
	Title     string
	User      *User
	CSRF      string
	Flash     *flash
	Query     string
	Sort      string
	Stats     *cohortStats
	Rows      []listRow
	View      *studentView
	Form      studentForm
	Courses   []Course
	Units     []Unit
	Year      int
	NextYear  int
	Error     string
	Portal    string // "admin" or "student" on the sign-in pages
	IsAdmin   bool
	UnitRows  []unitRow
	CPOptions []int
}

type listRow struct {
	ID, Name, Course, WAM string
	First, Last, Email    string
	CreditPoints          int
	wamValue              float64 // unexported: used for sorting and statistics only
	hasWAM                bool
}

// cohortStats are the summary cards shown above the student list.
type cohortStats struct {
	Total       int
	AverageWAM  string
	NeedSupport int // students whose WAM is below 60
}

// unitRow is one line of the administrator's unit table.
type unitRow struct {
	Code, Title              string
	CreditPoints, Enrolments int
}

type enrolRow struct {
	UnitCode, Title     string
	Year, Semester, CP  int
	MarkText, MarkValue string
	Grade               string
}

type studentView struct {
	Student     Student
	CourseName  string
	Rows        []enrolRow
	WAMText     string
	CompletedCP int
	Admin       bool
	MinYear     int // range of years the enrol form allows
	MaxYear     int
}

type studentForm struct {
	Student Student
	Errors  ValidationErrors
	IsEdit  bool
}

// base fills the fields every page needs (signed-in user, CSRF token, flash).
func (s *Server) base(r *http.Request, title string) pageData {
	cat := s.cat.Current()
	now := time.Now().Year()
	d := pageData{Title: title, Courses: cat.Courses, Units: cat.Units, Year: now, NextYear: now + 1, CPOptions: CreditPointOptions}
	if token, sess, ok := s.sessionFrom(r); ok {
		u := sess.User
		d.User, d.CSRF, d.IsAdmin = &u, sess.CSRF, u.Role == RoleAdmin
		d.Flash = s.sessions.TakeFlash(token)
	}
	return d
}

// render executes the template into a buffer first, so a template error can
// still produce a clean 500 page rather than a half-written response.
func (s *Server) render(w http.ResponseWriter, status int, name string, d pageData) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, d); err != nil {
		s.log.Error("template failed", "template", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// fail shows the error page with the given HTTP status.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	d := s.base(r, http.StatusText(status))
	d.Error = msg
	s.render(w, status, "error", d)
}

// notify queues a flash message for the next page view.
func (s *Server) notify(r *http.Request, kind, msg string) {
	if token, _, ok := s.sessionFrom(r); ok {
		s.sessions.SetFlash(token, flash{Kind: kind, Msg: msg})
	}
}

// buildView computes the WAM, grades and display text for one student.
func (s *Server) buildView(st Student, admin bool) *studentView {
	cat := s.cat.Current()
	now := time.Now().Year()
	v := &studentView{Student: st, Admin: admin, WAMText: "n/a", MinYear: now, MaxYear: now + 1}
	if admin {
		v.MinYear, v.MaxYear = 2000, 2100
	}
	if c, ok := cat.Course(st.CourseCode); ok {
		v.CourseName = c.Name
	}
	if wam, cp, ok := st.WAM(cat); ok {
		v.WAMText, v.CompletedCP = fmt.Sprintf("%.2f", wam), cp
	}
	enrolments := slices.Clone(st.Enrolments)
	slices.SortFunc(enrolments, func(a, b Enrolment) int {
		if a.Year != b.Year {
			return a.Year - b.Year
		}
		if a.Semester != b.Semester {
			return a.Semester - b.Semester
		}
		return strings.Compare(a.UnitCode, b.UnitCode)
	})
	for _, e := range enrolments {
		u, _ := cat.Unit(e.UnitCode)
		row := enrolRow{UnitCode: e.UnitCode, Title: u.Title, Year: e.Year, Semester: e.Semester, CP: u.CreditPoints, MarkText: "In progress", Grade: "-"}
		if e.Mark != nil {
			row.MarkText, row.MarkValue, row.Grade = strconv.Itoa(*e.Mark), strconv.Itoa(*e.Mark), Grade(*e.Mark)
		}
		v.Rows = append(v.Rows, row)
	}
	return v
}

// ---------------------------------------------------------------------------
// Handlers: sign in / out
// ---------------------------------------------------------------------------

// root is the landing page: the sign-in chooser for visitors, the home page
// (student list or own record) for people who are already signed in.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.sessionFrom(r); ok {
		s.home(w, r)
		return
	}
	s.render(w, http.StatusOK, "welcome", s.base(r, "Welcome"))
}

func portalTitle(role Role) string {
	if role == RoleAdmin {
		return "Administrator sign in"
	}
	return "Student sign in"
}

// loginForm returns the handler that shows the sign-in page for one role.
// Go has first-class functions, so one function can build both handlers.
func (s *Server) loginForm(portal Role) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := s.base(r, portalTitle(portal))
		d.Portal = portal.String()
		s.render(w, http.StatusOK, "login", d)
	}
}

// loginSubmit returns the handler that checks credentials for one role. The
// administrator page rejects student accounts and the student page rejects
// administrators, so each portal only lets its own role in.
func (s *Server) loginSubmit(portal Role) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		showError := func(msg string) {
			d := s.base(r, portalTitle(portal))
			d.Portal, d.Error = portal.String(), msg
			s.render(w, http.StatusUnauthorized, "login", d)
		}
		u, err := s.users.Authenticate(strings.TrimSpace(r.PostFormValue("username")), r.PostFormValue("password"))
		switch {
		case errors.Is(err, ErrBadLogin):
			showError("Incorrect username or password.")
			return
		case err != nil:
			s.log.Error("login failed", "error", err)
			s.fail(w, r, http.StatusInternalServerError, "Could not sign you in.")
			return
		case u.Role != portal:
			// Only reached with a CORRECT password, so this reveals nothing to a guesser.
			showError("These details belong to the " + u.Role.String() + " sign-in. Go back and choose it.")
			return
		}
		token, sess := s.sessions.Create(u)
		http.SetCookie(w, &http.Cookie{
			Name: "session", Value: token, Path: "/", HttpOnly: true,
			SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
			Expires: sess.Expires,
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("session"); err == nil {
		s.sessions.Destroy(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Handlers: viewing
// ---------------------------------------------------------------------------

// listRows builds the rows for the student list: filtered by the search text q
// and ordered by sortKey ("id" by default, "name", "course", or "wam" which is
// highest first with students who have no WAM last).
func (s *Server) listRows(q, sortKey string) ([]listRow, error) {
	all, err := s.repo.All()
	if err != nil {
		return nil, err
	}
	cat := s.cat.Current()
	q = strings.ToLower(strings.TrimSpace(q))
	var rows []listRow
	for _, st := range all {
		hay := strings.ToLower(st.ID + " " + st.FullName() + " " + st.CourseCode)
		if q != "" && !strings.Contains(hay, q) {
			continue
		}
		row := listRow{ID: st.ID, Name: st.FullName(), First: st.FirstName, Last: st.LastName, Email: st.Email, Course: st.CourseCode, WAM: "n/a"}
		if wam, cp, ok := st.WAM(cat); ok {
			row.WAM, row.CreditPoints, row.wamValue, row.hasWAM = fmt.Sprintf("%.2f", wam), cp, wam, true
		}
		rows = append(rows, row)
	}
	// Stable sort: ties keep the ID order that the repository already returns.
	slices.SortStableFunc(rows, func(a, b listRow) int {
		switch sortKey {
		case "name":
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		case "course":
			return strings.Compare(a.Course, b.Course)
		case "wam":
			switch {
			case a.hasWAM && !b.hasWAM:
				return -1
			case !a.hasWAM && b.hasWAM:
				return 1
			}
			return cmp.Compare(b.wamValue, a.wamValue)
		}
		return strings.Compare(a.ID, b.ID)
	})
	return rows, nil
}

// summarise computes the summary cards from the rows being shown.
func summarise(rows []listRow) cohortStats {
	st := cohortStats{Total: len(rows), AverageWAM: "n/a"}
	var sum float64
	var n int
	for _, r := range rows {
		if !r.hasWAM {
			continue
		}
		sum += r.wamValue
		n++
		if r.wamValue < 60 {
			st.NeedSupport++
		}
	}
	if n > 0 {
		st.AverageWAM = fmt.Sprintf("%.2f", sum/float64(n))
	}
	return st
}

// home lists all students (search, sort, summary cards) for admins and sends
// students to their own record.
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	_, sess, _ := s.sessionFrom(r)
	if sess.User.Role != RoleAdmin {
		http.Redirect(w, r, "/students/"+sess.User.StudentID, http.StatusSeeOther)
		return
	}
	rows, err := s.listRows(r.URL.Query().Get("q"), r.URL.Query().Get("sort"))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not load students.")
		return
	}
	stats := summarise(rows)
	d := s.base(r, "Students")
	d.Query, d.Sort, d.Rows, d.Stats = r.URL.Query().Get("q"), r.URL.Query().Get("sort"), rows, &stats
	s.render(w, http.StatusOK, "students", d)
}

// csvSafe stops spreadsheet programs from treating a cell as a formula: a value
// that starts with = + - @ (or a tab/carriage return) gets a leading apostrophe.
func csvSafe(v string) string {
	if v != "" && strings.ContainsRune("=+-@\t\r", rune(v[0])) {
		return "'" + v
	}
	return v
}

// exportCSV downloads the current (filtered, sorted) list as a CSV file.
func (s *Server) exportCSV(w http.ResponseWriter, r *http.Request) {
	rows, err := s.listRows(r.URL.Query().Get("q"), r.URL.Query().Get("sort"))
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not load students.")
		return
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"Student ID", "First name", "Last name", "Email", "Course", "Credit points completed", "WAM"})
	for _, row := range rows {
		_ = cw.Write([]string{csvSafe(row.ID), csvSafe(row.First), csvSafe(row.Last), csvSafe(row.Email), csvSafe(row.Course), strconv.Itoa(row.CreditPoints), row.WAM})
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		s.log.Error("csv export", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "Could not create the export.")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="students.csv"`)
	_, _ = buf.WriteTo(w)
}

// ---------------------------------------------------------------------------
// Handlers: change password (any signed-in user)
// ---------------------------------------------------------------------------

func (s *Server) passwordForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, "password", s.base(r, "Change password"))
}

func (s *Server) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	_, sess, _ := s.sessionFrom(r)
	current, next, confirm := r.PostFormValue("current"), r.PostFormValue("new"), r.PostFormValue("confirm")
	show := func(msg string) {
		d := s.base(r, "Change password")
		d.Error = msg
		s.render(w, http.StatusUnprocessableEntity, "password", d)
	}
	if next != confirm {
		show("The new passwords do not match.")
		return
	}
	err := s.users.SetPassword(sess.User.Username, current, next)
	var verrs ValidationErrors
	switch {
	case errors.Is(err, ErrBadLogin):
		show("Your current password is incorrect.")
	case errors.As(err, &verrs):
		show(strings.Join(slices.Sorted(maps.Values(verrs)), " "))
	case err != nil:
		s.log.Error("change password", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "Could not change the password.")
	default:
		s.notify(r, "ok", "Password changed.")
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// showStudent: admins may view anyone; a student may only view their own record.
func (s *Server) showStudent(w http.ResponseWriter, r *http.Request) {
	_, sess, _ := s.sessionFrom(r)
	id := r.PathValue("id")
	isAdmin := sess.User.Role == RoleAdmin
	if !isAdmin && sess.User.StudentID != id {
		s.fail(w, r, http.StatusForbidden, "You can only view your own record.")
		return
	}
	st, err := s.repo.Find(id)
	if errors.Is(err, ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "No student with that ID.")
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not load the record.")
		return
	}
	d := s.base(r, st.FullName())
	d.View = s.buildView(st, isAdmin)
	s.render(w, http.StatusOK, "student", d)
}

// ---------------------------------------------------------------------------
// Handlers: admin create / edit / delete
// ---------------------------------------------------------------------------

func (s *Server) newStudentForm(w http.ResponseWriter, r *http.Request) {
	d := s.base(r, "Add student")
	d.Form = studentForm{Errors: ValidationErrors{}}
	s.render(w, http.StatusOK, "form", d)
}

// studentFromForm copies the personal fields out of a submitted form.
func studentFromForm(r *http.Request) Student {
	return Student{
		ID:          r.PostFormValue("id"),
		FirstName:   r.PostFormValue("firstName"),
		LastName:    r.PostFormValue("lastName"),
		Email:       r.PostFormValue("email"),
		DateOfBirth: r.PostFormValue("dateOfBirth"),
		Phone:       r.PostFormValue("phone"),
		CourseCode:  r.PostFormValue("courseCode"),
	}
}

// showFormErrors re-displays the form with the user's input and the messages.
func (s *Server) showFormErrors(w http.ResponseWriter, r *http.Request, st Student, errs ValidationErrors, isEdit bool) {
	d := s.base(r, "Fix the highlighted fields")
	d.Form = studentForm{Student: st, Errors: errs, IsEdit: isEdit}
	s.render(w, http.StatusUnprocessableEntity, "form", d)
}

func (s *Server) createStudent(w http.ResponseWriter, r *http.Request) {
	st := studentFromForm(r)
	st.Normalise()
	err := s.repo.Add(st)

	var verrs ValidationErrors
	switch {
	case errors.As(err, &verrs):
		s.showFormErrors(w, r, st, verrs, false)
		return
	case errors.Is(err, ErrDuplicate):
		s.showFormErrors(w, r, st, ValidationErrors{"id": "A student with this ID already exists."}, false)
		return
	case err != nil:
		s.log.Error("add student", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "Could not save the student.")
		return
	}

	// Give the new student a login: username = student ID, random temporary password.
	temp := rand.Text()[:12]
	if err := s.users.Create(st.ID, temp, RoleStudent, st.ID); err != nil {
		s.notify(r, "error", "Student saved, but the login account could not be created: "+err.Error())
	} else {
		s.notify(r, "ok", fmt.Sprintf("Student %s added. Temporary login - username %s, password %s (shown once).", st.FullName(), st.ID, temp))
	}
	http.Redirect(w, r, "/students/"+st.ID, http.StatusSeeOther)
}

func (s *Server) editStudentForm(w http.ResponseWriter, r *http.Request) {
	st, err := s.repo.Find(r.PathValue("id"))
	if errors.Is(err, ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "No student with that ID.")
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not load the record.")
		return
	}
	d := s.base(r, "Edit "+st.FullName())
	d.Form = studentForm{Student: st, Errors: ValidationErrors{}, IsEdit: true}
	s.render(w, http.StatusOK, "form", d)
}

func (s *Server) updateStudent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in := studentFromForm(r)
	in.ID = id // the ID is never taken from the form on update
	err := s.repo.Modify(id, func(st *Student) error {
		st.FirstName, st.LastName, st.Email = in.FirstName, in.LastName, in.Email
		st.DateOfBirth, st.Phone, st.CourseCode = in.DateOfBirth, in.Phone, in.CourseCode
		return nil
	})
	s.afterChange(w, r, id, "Record updated.", err, in, true)
}

func (s *Server) deleteStudent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.repo.Delete(id)
	if errors.Is(err, ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "No student with that ID.")
		return
	}
	if err != nil {
		s.log.Error("delete student", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "Could not delete the student.")
		return
	}
	if err := s.users.Remove(id); err != nil {
		s.log.Error("remove user", "error", err)
	}
	s.notify(r, "ok", "Student "+id+" deleted.")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Handlers: enrolments and marks (all go through Modify, so they are validated)
// ---------------------------------------------------------------------------

// canManage says whether the signed-in user may change student id's enrolments:
// an administrator may change anyone's, a student only their own.
func (s *Server) canManage(r *http.Request, id string) (admin, allowed bool) {
	_, sess, ok := s.sessionFrom(r)
	if !ok {
		return false, false
	}
	if sess.User.Role == RoleAdmin {
		return true, true
	}
	return false, sess.User.StudentID == id
}

// passMark is the lowest mark that counts as a pass.
const passMark = 50

func (s *Server) enrol(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	admin, allowed := s.canManage(r, id)
	if !allowed {
		s.fail(w, r, http.StatusForbidden, "You can only change your own enrolments.")
		return
	}
	year, err1 := strconv.Atoi(r.PostFormValue("year"))
	sem, err2 := strconv.Atoi(r.PostFormValue("semester"))
	if err := errors.Join(err1, err2); err != nil {
		s.notify(r, "error", "Year and semester must be whole numbers.")
		http.Redirect(w, r, "/students/"+id, http.StatusSeeOther)
		return
	}
	now := time.Now().Year()
	if !admin && (year < now || year > now+1) {
		s.notify(r, "error", fmt.Sprintf("Students can enrol for %d or %d only.", now, now+1))
		http.Redirect(w, r, "/students/"+id, http.StatusSeeOther)
		return
	}
	unit := r.PostFormValue("unit")
	err := s.repo.Modify(id, func(st *Student) error {
		if !admin {
			for _, e := range st.Enrolments {
				if e.UnitCode == unit && e.Mark != nil && *e.Mark >= passMark {
					return ValidationErrors{"enrolments": "You have already passed " + unit + "."}
				}
			}
		}
		st.Enrolments = append(st.Enrolments, Enrolment{UnitCode: unit, Year: year, Semester: sem})
		return nil
	})
	s.afterChange(w, r, id, "Enrolled in "+unit+".", err, Student{}, false)
}

func (s *Server) setMark(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	unit := r.PostFormValue("unit")
	year, err1 := strconv.Atoi(r.PostFormValue("year"))
	sem, err2 := strconv.Atoi(r.PostFormValue("semester"))
	var mark *int // an empty box clears the mark
	var err3 error
	if txt := strings.TrimSpace(r.PostFormValue("mark")); txt != "" {
		var m int
		if m, err3 = strconv.Atoi(txt); err3 == nil {
			mark = &m
		}
	}
	if err := errors.Join(err1, err2, err3); err != nil {
		s.notify(r, "error", "Mark, year and semester must be whole numbers.")
		http.Redirect(w, r, "/students/"+id, http.StatusSeeOther)
		return
	}
	err := s.repo.Modify(id, func(st *Student) error {
		i := slices.IndexFunc(st.Enrolments, func(e Enrolment) bool {
			return e.UnitCode == unit && e.Year == year && e.Semester == sem
		})
		if i < 0 {
			return ValidationErrors{"enrolments": fmt.Sprintf("%s %d S%d is not in this student's enrolments.", unit, year, sem)}
		}
		st.Enrolments[i].Mark = mark
		return nil
	})
	s.afterChange(w, r, id, "Mark saved for "+unit+".", err, Student{}, false)
}

func (s *Server) unenrol(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	admin, allowed := s.canManage(r, id)
	if !allowed {
		s.fail(w, r, http.StatusForbidden, "You can only change your own enrolments.")
		return
	}
	unit := r.PostFormValue("unit")
	year, err1 := strconv.Atoi(r.PostFormValue("year"))
	sem, err2 := strconv.Atoi(r.PostFormValue("semester"))
	if err := errors.Join(err1, err2); err != nil {
		s.notify(r, "error", "Year and semester must be whole numbers.")
		http.Redirect(w, r, "/students/"+id, http.StatusSeeOther)
		return
	}
	err := s.repo.Modify(id, func(st *Student) error {
		i := slices.IndexFunc(st.Enrolments, func(e Enrolment) bool {
			return e.UnitCode == unit && e.Year == year && e.Semester == sem
		})
		if i < 0 {
			return ValidationErrors{"enrolments": unit + " is not in the enrolments."}
		}
		if !admin && st.Enrolments[i].Mark != nil {
			return ValidationErrors{"enrolments": unit + " already has a result, so only an administrator can remove it."}
		}
		st.Enrolments = slices.Delete(st.Enrolments, i, i+1)
		return nil
	})
	s.afterChange(w, r, id, "Removed "+unit+".", err, Student{}, false)
}

// ---------------------------------------------------------------------------
// Handlers: unit management (administrators only)
// ---------------------------------------------------------------------------

// enrolmentCounts returns how many enrolments use each unit code.
func (s *Server) enrolmentCounts() (map[string]int, error) {
	all, err := s.repo.All()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, st := range all {
		for _, e := range st.Enrolments {
			counts[e.UnitCode]++
		}
	}
	return counts, nil
}

func (s *Server) unitsPage(w http.ResponseWriter, r *http.Request) {
	counts, err := s.enrolmentCounts()
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "Could not load units.")
		return
	}
	d := s.base(r, "Units")
	for _, u := range d.Units {
		d.UnitRows = append(d.UnitRows, unitRow{Code: u.Code, Title: u.Title, CreditPoints: u.CreditPoints, Enrolments: counts[u.Code]})
	}
	s.render(w, http.StatusOK, "units", d)
}

// unitOutcome turns the result of a catalogue change into a flash message and
// sends the administrator back to the units page.
func (s *Server) unitOutcome(w http.ResponseWriter, r *http.Request, okMsg string, err error) {
	var verrs ValidationErrors
	switch {
	case err == nil:
		s.notify(r, "ok", okMsg)
	case errors.As(err, &verrs):
		s.notify(r, "error", strings.Join(slices.Sorted(maps.Values(verrs)), " "))
	case errors.Is(err, ErrUnitExists):
		s.notify(r, "error", "A unit with that code already exists.")
	case errors.Is(err, ErrUnitNotFound):
		s.notify(r, "error", "That unit does not exist.")
	default:
		s.log.Error("unit change", "error", err)
		s.notify(r, "error", "Could not save the change. Please try again.")
	}
	http.Redirect(w, r, "/units", http.StatusSeeOther)
}

// creditPoints reads the credit-point field, reporting a bad value as an error.
func creditPoints(r *http.Request) (int, error) {
	return strconv.Atoi(strings.TrimSpace(r.PostFormValue("creditPoints")))
}

func (s *Server) createUnit(w http.ResponseWriter, r *http.Request) {
	cp, err := creditPoints(r)
	if err != nil {
		s.unitOutcome(w, r, "", ValidationErrors{"creditPoints": "Credit points must be a whole number."})
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.PostFormValue("code")))
	err = s.cat.AddUnit(Unit{Code: code, Title: r.PostFormValue("title"), CreditPoints: cp})
	s.unitOutcome(w, r, "Unit "+code+" created.", err)
}

func (s *Server) updateUnit(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	cp, err := creditPoints(r)
	if err != nil {
		s.unitOutcome(w, r, "", ValidationErrors{"creditPoints": "Credit points must be a whole number."})
		return
	}
	err = s.cat.UpdateUnit(code, r.PostFormValue("title"), cp)
	s.unitOutcome(w, r, "Unit "+code+" updated. Every WAM is recalculated from the new credit points.", err)
}

// deleteUnit removes a unit only if no enrolment uses it. To stay safe when a
// student enrols at the same moment, it removes the unit FIRST (so new
// enrolments are refused by validation), then checks the students, and puts the
// unit back if any enrolment slipped in. This avoids holding two locks at once.
func (s *Server) deleteUnit(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	unit, ok := s.cat.Current().Unit(code)
	if !ok {
		s.unitOutcome(w, r, "", ErrUnitNotFound)
		return
	}
	if err := s.cat.RemoveUnit(code); err != nil {
		s.unitOutcome(w, r, "", err)
		return
	}
	counts, err := s.enrolmentCounts()
	if err != nil || counts[code] > 0 {
		if rerr := s.cat.AddUnit(unit); rerr != nil {
			s.log.Error("could not restore unit", "code", code, "error", rerr)
		}
		if err != nil {
			s.unitOutcome(w, r, "", err)
			return
		}
		s.unitOutcome(w, r, "", ValidationErrors{"unit": fmt.Sprintf("%s is still used by %d enrolment(s). Remove those enrolments first.", code, counts[code])})
		return
	}
	s.unitOutcome(w, r, "Unit "+code+" removed.", nil)
}

// afterChange turns the result of a Modify call into the right response:
// success -> flash + redirect; validation problem -> message (or the edit form);
// unknown student -> 404; anything else -> 500.
func (s *Server) afterChange(w http.ResponseWriter, r *http.Request, id, okMsg string, err error, input Student, fromEditForm bool) {
	var verrs ValidationErrors
	switch {
	case err == nil:
		s.notify(r, "ok", okMsg)
	case errors.Is(err, ErrNotFound):
		s.fail(w, r, http.StatusNotFound, "No student with that ID.")
		return
	case errors.As(err, &verrs):
		if fromEditForm {
			s.showFormErrors(w, r, input, verrs, true)
			return
		}
		s.notify(r, "error", strings.Join(slices.Sorted(maps.Values(verrs)), " "))
	default:
		// Storage errors can contain file paths, so log them and show a generic message.
		s.log.Error("modify student", "id", id, "error", err)
		s.notify(r, "error", "Could not save the change. Please try again.")
	}
	http.Redirect(w, r, "/students/"+id, http.StatusSeeOther)
}
