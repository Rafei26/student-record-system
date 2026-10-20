<!-- //Rafei Ahamed - 10696700  -->
# Student Record System (Go)

A small web application for managing student records, unit enrolments and WAM
(Weighted Average Mark). Built for CSP3341 Programming Languages and Paradigms,
Assessment 2. There is nothing to download besides Go itself.


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
| Student sign-in (Daniel Kumar) | `10000002` | `Student#2026` |
| Student sign-in (Rafei Ahamed) | `10000003` | `Student#2026` |


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
