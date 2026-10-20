# Student Record System (Go)

A small web application for managing student records, unit enrolments and WAM
(Weighted Average Mark). Built for CSP3341 Programming Languages and Paradigms,
Assessment 2. It uses **only the Go standard library** - there is nothing to
download besides Go itself.

## Features

| Area | What it does |
|---|---|
| Two sign-in portals | A landing page lets you choose **Administrator** or **Student**; each portal only accepts accounts of its own role |
| GUI | Browser-based interface (HTML forms rendered by `html/template`) |
| Storage | Persistent JSON files in `data/` (`students.json`, `users.json`, `units.json`), written atomically |
| Administrator | Create, edit and remove students; create, edit and remove **units**; enrol or unenrol any student; enter marks; search, sort, summary cards and **CSV export** |
| Student | View own record, WAM, enrolled units and grades; **enrol in units and remove own units** (this year or next; a unit with a result can only be removed by an administrator; a passed unit cannot be retaken); change own password |
| Units | Unit code (3 letters + 4 digits), title, credit points (5-30). A unit cannot be removed while any student is enrolled in it |
| WAM | Credit-point-weighted average of all marked units, shown per student |
| Validation | Every field checked on the server; errors are shown beside the field |
| Security | Salted PBKDF2 password hashes, session cookies, CSRF tokens, escaped HTML output, spreadsheet-formula-safe CSV |

## Requirements

* Go **1.24 or newer** (developed against 1.24; also fine on 1.25, 1.26 and 1.27)
* Check with: `go version`

## Run it

```bash
cd student_record_system
go run .
```

Then open <http://localhost:8080> and choose **Administrator** or **Student**. On the
first run the program creates `data/` and fills it with three demo students.

| Portal | Username | Password |
|---|---|---|
| Administrator sign-in | `admin` | `Admin#2026` |
| Student sign-in (Aisha Perera) | `10000001` (Student ID) | `Student#2026` |
| Student sign-in (Daniel Nguyen) | `10000002` | `Student#2026` |
| Student sign-in (Mei Tanaka) | `10000003` | `Student#2026` |

> These demo passwords are public. Delete the `data/` folder or change them
> before using the program for anything real. A new student created by an admin
> gets a random temporary password, shown once on screen.

Options: `go run . -addr localhost:9000 -data ./mydata -seed=false`
Stop the server with `Ctrl+C` (it shuts down gracefully).

## Test it

```bash
go vet ./...
go test -race -v ./...          # unit + HTTP tests with the race detector
go test -cover ./...            # coverage summary
go run ./examples/language_tour # runnable Part A demonstrations
```

`-race` needs a C compiler on some platforms. On macOS, Xcode command line tools
(`xcode-select --install`) are enough. If `-race` is unavailable, run the tests
without it and say so in the report.

## Build a single executable

```bash
go build -o student_record_system .
./student_record_system
```

## Project structure

| File | Layer | Purpose |
|---|---|---|
| `main.go` | Entry point | Flags, wiring, demo seed data, graceful shutdown |
| `models.go` | Domain | `Student`, `Enrolment`, `Role`, validation, grade bands, WAM |
| `catalogue.go` | Domain | Editable unit catalogue (`CatalogueStore`), unit validation, `units.json` |
| `repository.go` | Persistence | `StudentRepository` interface, `JSONRepository`, audit decorator, atomic file write |
| `auth.go` | Security | Users, PBKDF2 hashing, sessions, flash messages |
| `server.go` | HTTP | Routes, middleware, role checks, two sign-in portals, student/unit handlers, sorting, CSV export, password change |
| `templates.go` | GUI | HTML templates and CSS (embedded in the binary) |
| `app_test.go` | Tests | 24 test functions covering logic, storage, concurrency, access control, sign-in portals, units, self-enrolment, password change, sorting and CSV |
| `examples/language_tour/` | Report | Short runnable demos of the Go features covered in Part A |

## Known limitations (kept deliberately small)

* Sessions live in memory, so restarting the server signs everyone out.
* Served over plain HTTP on localhost; deploy behind HTTPS for real use.
* No rate limiting on login attempts.
* Courses are fixed sample data in `models.go`; only units are editable.
* Data is held in memory and rewritten to one JSON file per change, which is fine
  for hundreds of students but would be replaced by a database at larger scale.
