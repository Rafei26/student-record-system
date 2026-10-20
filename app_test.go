// app_test.go - automated tests. Run with:  go test -race -v ./...
//
// The -race flag turns on Go's built-in data race detector, which is the main
// evidence for the concurrency claims made in the report.
package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func init() { pbkdf2Iterations = 1000 } // keep password hashing fast in tests

func intp(n int) *int { return &n }

func validStudent(id string) Student {
	return Student{ID: id, FirstName: "Test", LastName: "Student", Email: "t" + id + "@example.edu",
		DateOfBirth: "2004-01-31", CourseCode: "BCS"}
}

// --- Domain logic -----------------------------------------------------------

func TestWAM(t *testing.T) {
	cat := DefaultCatalogue()
	tests := []struct {
		name string
		enr  []Enrolment
		want float64
		ok   bool
	}{
		{"no enrolments", nil, 0, false},
		{"in progress only", []Enrolment{{"CSP1150", 2026, 1, nil}}, 0, false},
		{"single mark", []Enrolment{{"CSP1150", 2025, 1, intp(80)}}, 80, true},
		{"equal credit points", []Enrolment{{"CSP1150", 2025, 1, intp(70)}, {"CSI1101", 2025, 1, intp(81)}}, 75.5, true},
		{"unmarked units are ignored", []Enrolment{{"CSP1150", 2025, 1, intp(60)}, {"CSI1101", 2025, 1, nil}}, 60, true},
		{"rounds to 2 dp", []Enrolment{{"CSP1150", 2025, 1, intp(50)}, {"CSI1101", 2025, 1, intp(51)}, {"MAT1104", 2025, 1, intp(51)}}, 50.67, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, ok := Student{Enrolments: tc.enr}.WAM(cat)
			if ok != tc.ok || got != tc.want {
				t.Errorf("WAM = %v, %v; want %v, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestGradeBoundaries(t *testing.T) {
	for mark, want := range map[int]string{100: "HD", 80: "HD", 79: "D", 70: "D", 69: "C", 60: "C", 59: "P", 50: "P", 49: "N", 0: "N"} {
		if got := Grade(mark); got != want {
			t.Errorf("Grade(%d) = %s, want %s", mark, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	cat := DefaultCatalogue()
	tests := []struct {
		name   string
		mutate func(*Student)
		field  string // expected failing field, "" means valid
	}{
		{"valid", func(s *Student) {}, ""},
		{"short id", func(s *Student) { s.ID = "1234" }, "id"},
		{"letters in id", func(s *Student) { s.ID = "12ab5678" }, "id"},
		{"empty first name", func(s *Student) { s.FirstName = " " }, "firstName"},
		{"digit in name", func(s *Student) { s.LastName = "R2D2" }, "lastName"},
		{"bad email", func(s *Student) { s.Email = "not-an-email" }, "email"},
		{"bad date format", func(s *Student) { s.DateOfBirth = "31/01/2004" }, "dateOfBirth"},
		{"too young", func(s *Student) { s.DateOfBirth = time.Now().Format("2006-01-02") }, "dateOfBirth"},
		{"bad phone", func(s *Student) { s.Phone = "abc" }, "phone"},
		{"unknown course", func(s *Student) { s.CourseCode = "XYZ" }, "courseCode"},
		{"unknown unit", func(s *Student) { s.Enrolments = []Enrolment{{"NOPE999", 2025, 1, nil}} }, "enrolments"},
		{"mark above 100", func(s *Student) { s.Enrolments = []Enrolment{{"CSP1150", 2025, 1, intp(101)}} }, "enrolments"},
		{"bad semester", func(s *Student) { s.Enrolments = []Enrolment{{"CSP1150", 2025, 3, nil}} }, "enrolments"},
		{"duplicate enrolment", func(s *Student) {
			s.Enrolments = []Enrolment{{"CSP1150", 2025, 1, nil}, {"CSP1150", 2025, 1, nil}}
		}, "enrolments"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := validStudent("12345678")
			tc.mutate(&s)
			err := s.Validate(cat)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			var verrs ValidationErrors
			if !errors.As(err, &verrs) || verrs[tc.field] == "" {
				t.Fatalf("expected error on %q, got %v", tc.field, err)
			}
		})
	}
}

// --- Repository -------------------------------------------------------------

func TestRepositoryPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "students.json")
	repo, err := NewJSONRepository(path, DefaultCatalogue())
	if err != nil {
		t.Fatal(err)
	}
	s := validStudent("11111111")
	s.Enrolments = []Enrolment{{"CSP1150", 2025, 1, intp(75)}}
	if err := repo.Add(s); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewJSONRepository(path, DefaultCatalogue()) // simulates a restart
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Find("11111111")
	if err != nil || got.Enrolments[0].Mark == nil || *got.Enrolments[0].Mark != 75 {
		t.Fatalf("record did not survive restart: %+v, %v", got, err)
	}
}

func TestRepositoryErrors(t *testing.T) {
	repo, _ := NewJSONRepository("", DefaultCatalogue())
	if err := repo.Add(validStudent("22222222")); err != nil {
		t.Fatal(err)
	}
	if err := repo.Add(validStudent("22222222")); !errors.Is(err, ErrDuplicate) {
		t.Errorf("duplicate add: got %v, want ErrDuplicate", err)
	}
	if _, err := repo.Find("99999999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("find missing: got %v, want ErrNotFound", err)
	}
	if err := repo.Delete("99999999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: got %v, want ErrNotFound", err)
	}
	// An invalid edit must be rejected and must NOT change the stored record.
	err := repo.Modify("22222222", func(s *Student) error { s.Email = "broken"; return nil })
	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("invalid modify: got %v, want ValidationErrors", err)
	}
	if got, _ := repo.Find("22222222"); got.Email == "broken" {
		t.Error("rejected edit leaked into the repository")
	}
}

func TestCloneIsolation(t *testing.T) {
	repo, _ := NewJSONRepository("", DefaultCatalogue())
	s := validStudent("33333333")
	s.Enrolments = []Enrolment{{"CSP1150", 2025, 1, intp(60)}}
	_ = repo.Add(s)
	got, _ := repo.Find("33333333")
	*got.Enrolments[0].Mark = 1 // mutate the returned copy
	again, _ := repo.Find("33333333")
	if *again.Enrolments[0].Mark != 60 {
		t.Error("changing a returned copy altered the stored record")
	}
}

// TestConcurrentAdds starts 100 goroutines that write at the same time. With
// `go test -race` any missing lock would be reported as a data race.
func TestConcurrentAdds(t *testing.T) {
	repo, _ := NewJSONRepository(filepath.Join(t.TempDir(), "s.json"), DefaultCatalogue())
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := repo.Add(validStudent(fmt.Sprintf("%08d", 40000000+i))); err != nil {
				t.Errorf("add %d: %v", i, err)
			}
			_, _ = repo.All() // readers run alongside the writers
		}()
	}
	wg.Wait()
	if all, _ := repo.All(); len(all) != 100 {
		t.Fatalf("stored %d students, want 100", len(all))
	}
}

// The audit decorator satisfies the interface by embedding another repository.
func TestAuditDecoratorDelegates(t *testing.T) {
	inner, _ := NewJSONRepository("", DefaultCatalogue())
	var repo StudentRepository = auditedRepository{inner, slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := repo.Add(validStudent("55555555")); err != nil {
		t.Fatal(err)
	}
	if _, err := inner.Find("55555555"); err != nil {
		t.Error("decorator did not pass Add through to the wrapped repository")
	}
}

// --- Evidence for the "weak type modelling" disadvantage ---------------------

// Role is an integer underneath, so the compiler accepts values that are not in
// the enumeration. Only the runtime Valid() check catches them.
func TestRoleIsNotTypeSafe(t *testing.T) {
	bogus := Role(99) // compiles without any warning
	if bogus.Valid() {
		t.Fatal("Role(99) should not be valid")
	}
	if got := bogus.String(); got != "Role(99)" {
		t.Errorf("String() = %q", got)
	}
	if _, err := bogus.MarshalText(); err == nil {
		t.Error("an invalid role must not be written to disk")
	}
}

// A function returning a typed-nil map as an error would look like a failure.
func TestTypedNilGotcha(t *testing.T) {
	bad := func() error { var v ValidationErrors; return v } // the trap
	if bad() == nil {
		t.Fatal("expected the typed-nil trap to produce a non-nil error")
	}
	if err := validStudent("66666666").Validate(DefaultCatalogue()); err != nil {
		t.Errorf("Validate must return a real nil for valid input, got %v", err)
	}
}

// --- Users and sessions -----------------------------------------------------

func TestAuthenticate(t *testing.T) {
	users, _ := NewUserStore("")
	if err := users.Create("alice", "correct horse", RoleAdmin, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Authenticate("alice", "correct horse"); err != nil {
		t.Errorf("good login failed: %v", err)
	}
	for _, c := range [][2]string{{"alice", "wrong"}, {"nobody", "x"}} {
		if _, err := users.Authenticate(c[0], c[1]); !errors.Is(err, ErrBadLogin) {
			t.Errorf("Authenticate(%q) = %v, want ErrBadLogin", c[0], err)
		}
	}
	if err := users.Create("alice", "x", RoleAdmin, ""); !errors.Is(err, ErrUserExists) {
		t.Errorf("duplicate user: got %v", err)
	}
	if err := users.Create("eve", "x", Role(99), ""); err == nil {
		t.Error("an invalid role was accepted")
	}
}

// --- HTTP behaviour (end to end through the real handlers) -------------------

type testApp struct {
	ts  *httptest.Server
	cli *http.Client
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	cats, _ := NewCatalogueStore("", DefaultCatalogue())
	repo, _ := NewJSONRepository(filepath.Join(t.TempDir(), "s.json"), cats)
	users, _ := NewUserStore("")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seedDemoData(repo, users); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(repo, users, NewSessionManager(time.Hour), cats, log)
	ts := httptest.NewServer(srv.Routes())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	cli := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &testApp{ts: ts, cli: cli}
}

func (a *testApp) get(t *testing.T, path string) (int, string) {
	t.Helper()
	resp, err := a.cli.Get(a.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (a *testApp) post(t *testing.T, path string, form url.Values) (int, string, string) {
	t.Helper()
	resp, err := a.cli.PostForm(a.ts.URL+path, form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Location")
}

var csrfRe = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

func (a *testApp) login(t *testing.T, user, pass string) string {
	t.Helper()
	portal := "/login/student"
	if user == "admin" {
		portal = "/login/admin"
	}
	code, _, loc := a.post(t, portal, url.Values{"username": {user}, "password": {pass}})
	if code != http.StatusSeeOther {
		t.Fatalf("login %s: status %d (loc %q)", user, code, loc)
	}
	// Follow the redirect chain by hand (students go "/" -> "/students/<id>").
	var body string
	for range 3 {
		var code int
		code, body = a.get(t, loc)
		if code != http.StatusSeeOther {
			break
		}
		resp, _ := a.cli.Get(a.ts.URL + loc)
		loc = resp.Header.Get("Location")
		resp.Body.Close()
	}
	m := csrfRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no CSRF token found on page after login")
	}
	return m[1]
}

func TestAnonymousUsersSeeOnlyTheChooser(t *testing.T) {
	app := newTestApp(t)
	code, body := app.get(t, "/")
	if code != 200 || !strings.Contains(body, `href="/login/admin"`) || !strings.Contains(body, `href="/login/student"`) {
		t.Errorf("landing page: status %d, links to both sign-ins present = %v", code, strings.Contains(body, "/login/student"))
	}
	for _, path := range []string{"/students/10000001", "/units", "/export.csv", "/account/password"} {
		resp, err := app.cli.Get(app.ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
			t.Errorf("anonymous %s: got %d -> %q, want 303 -> /", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

func TestWrongPasswordIsRejected(t *testing.T) {
	app := newTestApp(t)
	code, body, _ := app.post(t, "/login/admin", url.Values{"username": {"admin"}, "password": {"nope"}})
	if code != http.StatusUnauthorized || !strings.Contains(body, "Incorrect username or password") {
		t.Errorf("got %d", code)
	}
}

func TestStudentAccessControl(t *testing.T) {
	app := newTestApp(t)
	csrf := app.login(t, "10000001", "Student#2026")

	if code, body := app.get(t, "/students/10000001"); code != 200 || !strings.Contains(body, "74.50") { // (78+71+85+64)/4
		t.Errorf("own record: status %d, WAM present=%v", code, strings.Contains(body, "74.50"))
	}
	if code, _ := app.get(t, "/students/10000002"); code != http.StatusForbidden {
		t.Errorf("another student's record: got %d, want 403", code)
	}
	if code, _ := app.get(t, "/students/new"); code != http.StatusForbidden {
		t.Errorf("admin form as student: got %d, want 403", code)
	}
	code, _, _ := app.post(t, "/students/10000001/delete", url.Values{"_csrf": {csrf}})
	if code != http.StatusForbidden {
		t.Errorf("delete as student: got %d, want 403", code)
	}
}

func TestAdminFlow(t *testing.T) {
	app := newTestApp(t)
	csrf := app.login(t, "admin", "Admin#2026")

	// A POST without the CSRF token is refused.
	if code, _, _ := app.post(t, "/students", url.Values{"id": {"10000009"}}); code != http.StatusForbidden {
		t.Errorf("missing CSRF token: got %d, want 403", code)
	}
	// Invalid input re-displays the form with 422 and field messages.
	code, body, _ := app.post(t, "/students", url.Values{"_csrf": {csrf}, "id": {"12"}, "email": {"x"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "exactly 8 digits") {
		t.Errorf("invalid add: got %d", code)
	}
	// A valid student is created and the admin is redirected to the record.
	add := url.Values{"_csrf": {csrf}, "id": {"10000009"}, "firstName": {"Nina"}, "lastName": {"Okafor"},
		"email": {"nina@example.edu"}, "dateOfBirth": {"2004-05-05"}, "courseCode": {"BCS"}}
	if code, _, loc := app.post(t, "/students", add); code != http.StatusSeeOther || loc != "/students/10000009" {
		t.Fatalf("valid add: got %d -> %q", code, loc)
	}
	// Duplicate IDs are refused.
	if code, _, _ := app.post(t, "/students", add); code != http.StatusUnprocessableEntity {
		t.Errorf("duplicate add: got %d, want 422", code)
	}
	// Enrol, mark and check the WAM shown on the page.
	enrol := url.Values{"_csrf": {csrf}, "unit": {"CSP1150"}, "year": {"2026"}, "semester": {"1"}}
	app.post(t, "/students/10000009/enrol", enrol)
	app.post(t, "/students/10000009/mark", url.Values{"_csrf": {csrf}, "unit": {"CSP1150"}, "year": {"2026"}, "semester": {"1"}, "mark": {"88"}})
	if _, body := app.get(t, "/students/10000009"); !strings.Contains(body, "88.00") || !strings.Contains(body, "HD") {
		t.Error("WAM 88.00 / grade HD not shown after marking")
	}
	// Search and delete.
	if _, body := app.get(t, "/?q=okafor"); !strings.Contains(body, "10000009") || strings.Contains(body, "Perera") {
		t.Error("search did not filter the list")
	}
	if code, _, _ := app.post(t, "/students/10000009/delete", url.Values{"_csrf": {csrf}}); code != http.StatusSeeOther {
		t.Errorf("delete: got %d", code)
	}
	if code, _ := app.get(t, "/students/10000009"); code != http.StatusNotFound {
		t.Errorf("deleted record: got %d, want 404", code)
	}
}

// --- Features added later: password change, sorting, statistics, CSV ----------

func TestSetPassword(t *testing.T) {
	users, _ := NewUserStore("")
	if err := users.Create("bob", "old-password", RoleStudent, "10000001"); err != nil {
		t.Fatal(err)
	}
	if err := users.SetPassword("bob", "wrong", "new-password-1"); !errors.Is(err, ErrBadLogin) {
		t.Errorf("wrong current password: got %v, want ErrBadLogin", err)
	}
	var verrs ValidationErrors
	if err := users.SetPassword("bob", "old-password", "short"); !errors.As(err, &verrs) {
		t.Errorf("short password: got %v, want ValidationErrors", err)
	}
	if err := users.SetPassword("bob", "old-password", "old-password"); !errors.As(err, &verrs) {
		t.Errorf("unchanged password: got %v, want ValidationErrors", err)
	}
	if err := users.SetPassword("bob", "old-password", "new-password-1"); err != nil {
		t.Fatalf("valid change failed: %v", err)
	}
	if _, err := users.Authenticate("bob", "old-password"); !errors.Is(err, ErrBadLogin) {
		t.Error("old password still works after a change")
	}
	if _, err := users.Authenticate("bob", "new-password-1"); err != nil {
		t.Errorf("new password rejected: %v", err)
	}
}

func TestChangePasswordOverHTTP(t *testing.T) {
	app := newTestApp(t)
	csrf := app.login(t, "10000003", "Student#2026")
	form := func(cur, nw, conf string) url.Values {
		return url.Values{"_csrf": {csrf}, "current": {cur}, "new": {nw}, "confirm": {conf}}
	}
	if code, body, _ := app.post(t, "/account/password", form("Student#2026", "NewPass#2027", "different")); code != 422 || !strings.Contains(body, "do not match") {
		t.Errorf("mismatched confirmation: got %d", code)
	}
	if code, body, _ := app.post(t, "/account/password", form("wrong", "NewPass#2027", "NewPass#2027")); code != 422 || !strings.Contains(body, "incorrect") {
		t.Errorf("wrong current password: got %d", code)
	}
	if code, _, _ := app.post(t, "/account/password", form("Student#2026", "NewPass#2027", "NewPass#2027")); code != http.StatusSeeOther {
		t.Fatalf("valid change: got %d, want 303", code)
	}
	// A fresh client (no cookies) can now sign in only with the new password.
	jar, _ := cookiejar.New(nil)
	fresh := &testApp{ts: app.ts, cli: &http.Client{Jar: jar, CheckRedirect: app.cli.CheckRedirect}}
	if code, _, _ := fresh.post(t, "/login/student", url.Values{"username": {"10000003"}, "password": {"Student#2026"}}); code != http.StatusUnauthorized {
		t.Errorf("old password after change: got %d, want 401", code)
	}
	if code, _, _ := fresh.post(t, "/login/student", url.Values{"username": {"10000003"}, "password": {"NewPass#2027"}}); code != http.StatusSeeOther {
		t.Errorf("new password: got %d, want 303", code)
	}
}

func TestListSortAndStats(t *testing.T) {
	cat := DefaultCatalogue()
	repo, _ := NewJSONRepository("", cat)
	add := func(id, first string, mark *int) {
		s := validStudent(id)
		s.FirstName = first
		if mark != nil {
			s.Enrolments = []Enrolment{{"CSP1150", 2025, 1, mark}}
		}
		if err := repo.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	add("20000001", "Zed", intp(50))
	add("20000002", "Amy", intp(90))
	add("20000003", "Bob", nil)
	store, _ := NewCatalogueStore("", cat)
	srv := &Server{repo: repo, cat: store}

	ids := func(sortKey, q string) string {
		rows, err := srv.listRows(q, sortKey)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range rows {
			out = append(out, r.ID[len(r.ID)-1:])
		}
		return strings.Join(out, "")
	}
	for _, tc := range []struct{ sort, q, want string }{
		{"", "", "123"},         // default: by ID
		{"name", "", "231"},     // Amy, Bob, Zed
		{"wam", "", "213"},      // 90, 50, then no WAM
		{"wam", "amy", "2"},     // search filter applies to the sort too
		{"nonsense", "", "123"}, // unknown key falls back to ID order
	} {
		if got := ids(tc.sort, tc.q); got != tc.want {
			t.Errorf("sort=%q q=%q: got order %s, want %s", tc.sort, tc.q, got, tc.want)
		}
	}
	rows, _ := srv.listRows("", "")
	if st := summarise(rows); st.Total != 3 || st.AverageWAM != "70.00" || st.NeedSupport != 1 {
		t.Errorf("stats = %+v, want 3 students, average 70.00, 1 below 60", st)
	}
	if st := summarise(nil); st.AverageWAM != "n/a" {
		t.Errorf("empty list average = %q, want n/a", st.AverageWAM)
	}
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{"=SUM(A1)": "'=SUM(A1)", "+1": "'+1", "-2": "'-2", "@x": "'@x", "Aisha": "Aisha", "": ""} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSVExportAndPermissions(t *testing.T) {
	app := newTestApp(t)
	app.login(t, "admin", "Admin#2026")
	resp, err := app.cli.Get(app.ts.URL + "/export.csv?sort=wam")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	body := string(b)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") {
		t.Fatalf("export: status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "Student ID,First name") {
		t.Fatalf("expected header + 3 rows, got %d lines:\n%s", len(lines), body)
	}
	if !strings.Contains(lines[1], "10000001,Aisha,Perera,aisha.perera@example.edu,BCS,60,74.50") {
		t.Errorf("first row (highest WAM) = %q", lines[1])
	}

	student := newTestApp(t)
	student.login(t, "10000001", "Student#2026")
	if code, _ := student.get(t, "/export.csv"); code != http.StatusForbidden {
		t.Errorf("export as student: got %d, want 403", code)
	}
}

// --- Two sign-in portals, unit management and student self-service -------------

func TestPortalsKeepRolesApart(t *testing.T) {
	app := newTestApp(t)
	for _, path := range []string{"/login/admin", "/login/student"} {
		if code, _ := app.get(t, path); code != 200 {
			t.Errorf("GET %s: %d", path, code)
		}
	}
	// Correct credentials, wrong door: refused with a hint, and no session is created.
	code, body, _ := app.post(t, "/login/admin", url.Values{"username": {"10000001"}, "password": {"Student#2026"}})
	if code != http.StatusUnauthorized || !strings.Contains(body, "student sign-in") {
		t.Errorf("student at admin portal: got %d", code)
	}
	code, body, _ = app.post(t, "/login/student", url.Values{"username": {"admin"}, "password": {"Admin#2026"}})
	if code != http.StatusUnauthorized || !strings.Contains(body, "admin sign-in") {
		t.Errorf("admin at student portal: got %d", code)
	}
	if code, _ := app.get(t, "/units"); code != http.StatusSeeOther {
		t.Errorf("a refused sign-in must not leave a session behind: /units gave %d", code)
	}
	// Unknown user gets the same message wherever they try.
	if code, body, _ := app.post(t, "/login/student", url.Values{"username": {"nobody"}, "password": {"x"}}); code != 401 || !strings.Contains(body, "Incorrect username or password") {
		t.Errorf("unknown user: %d", code)
	}
}

func TestCatalogueStoreRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "units.json")
	st, err := NewCatalogueStore(path, DefaultCatalogue())
	if err != nil {
		t.Fatal(err)
	}
	var verrs ValidationErrors
	for _, bad := range []Unit{
		{"csp44", "Too short code", 15}, {"CSP4401", "x", 15}, {"CSP4401", "Valid title", 12}, {"CSP4401", "Bad <script>", 15},
	} {
		if err := st.AddUnit(bad); !errors.As(err, &verrs) {
			t.Errorf("AddUnit(%+v) = %v, want ValidationErrors", bad, err)
		}
	}
	if err := st.AddUnit(Unit{"csp4401", "  Machine Learning  ", 20}); err != nil { // code and title are normalised
		t.Fatal(err)
	}
	if err := st.AddUnit(Unit{"CSP4401", "Duplicate", 15}); !errors.Is(err, ErrUnitExists) {
		t.Errorf("duplicate: got %v", err)
	}
	if err := st.UpdateUnit("CSP4401", "Applied Machine Learning", 25); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateUnit("ZZZ9999", "Nothing here", 15); !errors.Is(err, ErrUnitNotFound) {
		t.Errorf("update missing: got %v", err)
	}
	// The file is saved, and a new store loads the same units.
	again, err := NewCatalogueStore(path, DefaultCatalogue())
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := again.Current().Unit("CSP4401"); !ok || u.Title != "Applied Machine Learning" || u.CreditPoints != 25 {
		t.Errorf("reloaded unit = %+v, %v", u, ok)
	}
	// Current returns a copy: editing it must not change the store.
	snap := st.Current()
	snap.Units[0].Title = "TAMPERED"
	if st.Current().Units[0].Title == "TAMPERED" {
		t.Error("Current() leaked internal state")
	}
	if err := st.RemoveUnit("CSP4401"); err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Current().Unit("CSP4401"); ok {
		t.Error("unit still present after RemoveUnit")
	}
	if err := st.RemoveUnit("CSP4401"); !errors.Is(err, ErrUnitNotFound) {
		t.Errorf("remove twice: got %v", err)
	}
}

// page fetches a path and returns the body (the client does not follow redirects).
func (a *testApp) page(t *testing.T, path string) string {
	t.Helper()
	_, body := a.get(t, path)
	return body
}

func TestAdminManagesUnitsOverHTTP(t *testing.T) {
	app := newTestApp(t)
	csrf := app.login(t, "admin", "Admin#2026")
	post := func(path string, v url.Values) {
		v.Set("_csrf", csrf)
		if code, _, _ := app.post(t, path, v); code != http.StatusSeeOther {
			t.Fatalf("POST %s: %d", path, code)
		}
	}
	post("/units", url.Values{"code": {"csp4401"}, "title": {"Machine Learning"}, "creditPoints": {"15"}})
	if body := app.page(t, "/units"); !strings.Contains(body, "Unit CSP4401 created.") || !strings.Contains(body, "Machine Learning") {
		t.Error("created unit not shown")
	}
	post("/units", url.Values{"code": {"csp4401"}, "title": {"Again"}, "creditPoints": {"15"}})
	if !strings.Contains(app.page(t, "/units"), "already exists") {
		t.Error("duplicate unit was not refused")
	}
	post("/units", url.Values{"code": {"bad"}, "title": {"x"}, "creditPoints": {"7"}})
	if !strings.Contains(app.page(t, "/units"), "three letters then four digits") {
		t.Error("invalid unit was not refused with a message")
	}
	post("/units/CSP4401", url.Values{"title": {"Applied Machine Learning"}, "creditPoints": {"20"}})
	if !strings.Contains(app.page(t, "/units"), "Applied Machine Learning") {
		t.Error("edited title not shown")
	}
	// A unit that a student uses cannot be removed, and must still exist afterwards.
	year := time.Now().Year()
	post("/students/10000003/enrol", url.Values{"unit": {"CSP4401"}, "year": {fmt.Sprint(year)}, "semester": {"1"}})
	post("/units/CSP4401/delete", url.Values{})
	if body := app.page(t, "/units"); !strings.Contains(body, "still used by 1 enrolment") || !strings.Contains(body, "Applied Machine Learning") {
		t.Error("unit in use was removed or the refusal message is missing")
	}
	// Remove the enrolment, then the unit can go.
	post("/students/10000003/unenrol", url.Values{"unit": {"CSP4401"}, "year": {fmt.Sprint(year)}, "semester": {"1"}})
	post("/units/CSP4401/delete", url.Values{})
	if body := app.page(t, "/units"); !strings.Contains(body, "Unit CSP4401 removed.") || strings.Contains(body, "Applied Machine Learning") {
		t.Error("unused unit was not removed")
	}
	// Students never see the unit pages.
	stu := newTestApp(t)
	stu.login(t, "10000001", "Student#2026")
	if code, _ := stu.get(t, "/units"); code != http.StatusForbidden {
		t.Errorf("student GET /units: %d, want 403", code)
	}
}

func TestStudentSelfEnrolment(t *testing.T) {
	app := newTestApp(t)
	csrf := app.login(t, "10000001", "Student#2026") // Aisha: has passed CSP1150 (78), has CSI2132 2026 S1 marked 64, CSP3341 2026 S2 unmarked
	year := time.Now().Year()
	post := func(path string, v url.Values) (int, string) {
		v.Set("_csrf", csrf)
		code, body, _ := app.post(t, path, v)
		return code, body
	}
	enrol := func(unit string, y, sem int) url.Values {
		return url.Values{"unit": {unit}, "year": {fmt.Sprint(y)}, "semester": {fmt.Sprint(sem)}}
	}
	own := "/students/10000001"

	if code, _ := post(own+"/enrol", enrol("CSI3207", year, 1)); code != http.StatusSeeOther {
		t.Fatalf("own enrol: %d", code)
	}
	if body := app.page(t, own); !strings.Contains(body, "Enrolled in CSI3207.") || !strings.Contains(body, "Software Engineering") {
		t.Error("new enrolment not visible on the student's own page")
	}
	post(own+"/enrol", enrol("CSP2104", year-5, 1))
	if !strings.Contains(app.page(t, own), "Students can enrol for") {
		t.Error("year outside the allowed range was not refused")
	}
	post(own+"/enrol", enrol("CSP1150", year, 2))
	if !strings.Contains(app.page(t, own), "already passed CSP1150") {
		t.Error("re-enrolling in a passed unit was not refused")
	}
	post(own+"/enrol", enrol("CSI3207", year, 1))
	if !strings.Contains(app.page(t, own), "already enrolled") {
		t.Error("duplicate enrolment was not refused")
	}
	post(own+"/unenrol", enrol("CSI3207", year, 1))
	if !strings.Contains(app.page(t, own), "Removed CSI3207.") {
		t.Error("student could not remove an unmarked unit")
	}
	post(own+"/unenrol", enrol("CSI2132", 2026, 1)) // has a mark of 64
	if !strings.Contains(app.page(t, own), "already has a result") {
		t.Error("student removed a unit that has a result")
	}
	// Other students' records, and marks, are off limits.
	if code, _ := post("/students/10000002/enrol", enrol("CSI3207", year, 1)); code != http.StatusForbidden {
		t.Errorf("enrol another student: %d, want 403", code)
	}
	if code, _ := post(own+"/mark", url.Values{"unit": {"CSP3341"}, "year": {"2026"}, "semester": {"2"}, "mark": {"100"}}); code != http.StatusForbidden {
		t.Errorf("student setting a mark: %d, want 403", code)
	}
	// The student page offers the enrol form but no mark or edit controls.
	body := app.page(t, own)
	if !strings.Contains(body, "Enrol in a unit") || strings.Contains(body, `name="mark"`) || strings.Contains(body, "Edit details") {
		t.Error("student page shows the wrong controls")
	}
}
