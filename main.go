// Student Record System - a small web application written in Go.
//
// main.go wires the layers together and starts the HTTP server:
//
//	browser (HTML forms) -> server.go -> repository.go -> data/students.json
//	                                  -> auth.go       -> data/users.json
//	                                  -> catalogue.go  -> data/units.json
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// run holds the real logic so main stays tiny and errors have a single exit point.
func run() error {
	addr := flag.String("addr", "localhost:8080", "address to listen on")
	dataDir := flag.String("data", "data", "folder for students.json, users.json and units.json")
	seed := flag.Bool("seed", true, "create demo students and accounts if no data exists yet")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cats, err := NewCatalogueStore(filepath.Join(*dataDir, "units.json"), DefaultCatalogue())
	if err != nil {
		return fmt.Errorf("open unit data: %w", err)
	}
	jsonRepo, err := NewJSONRepository(filepath.Join(*dataDir, "students.json"), cats)
	if err != nil {
		return fmt.Errorf("open student data: %w", err)
	}
	users, err := NewUserStore(filepath.Join(*dataDir, "users.json"))
	if err != nil {
		return fmt.Errorf("open user data: %w", err)
	}
	if *seed {
		if err := seedDemoData(jsonRepo, users); err != nil {
			return fmt.Errorf("seed demo data: %w", err)
		}
	}

	// The audit decorator wraps the real repository; the server cannot tell.
	repo := auditedRepository{StudentRepository: jsonRepo, log: logger}
	srv := NewServer(repo, users, NewSessionManager(8*time.Hour), cats, logger)

	httpServer := &http.Server{
		Addr:              *addr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Event handling in Go: the OS signal (Ctrl+C) arrives as a context cancellation.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Serve in a goroutine and report its result over a channel.
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.ListenAndServe() }()
	logger.Info("server started", "url", "http://"+*addr)

	select { // wait for whichever event happens first
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	return nil
}

// seedDemoData adds sample students and logins on the very first run only.
// DEMO PASSWORDS ARE PUBLIC - change or remove them for any real use.
func seedDemoData(repo StudentRepository, users *UserStore) error {
	existing, err := repo.All()
	if err != nil {
		return err
	}
	if len(existing) > 0 || users.Count() > 0 {
		return nil
	}
	mark := func(n int) *int { return &n }
	demo := []Student{
		{ID: "10000001", FirstName: "Aisha", LastName: "Perera", Email: "aisha.perera@example.edu", DateOfBirth: "2004-03-14", Phone: "+61400000001", CourseCode: "BCS",
			Enrolments: []Enrolment{
				{"CSP1150", 2025, 1, mark(78)}, {"CSI1101", 2025, 1, mark(71)}, {"CSP2348", 2025, 2, mark(85)},
				{"CSI2132", 2026, 1, mark(64)}, {"CSP3341", 2026, 2, nil},
			}},
		{ID: "10000002", FirstName: "Daniel", LastName: "Nguyen", Email: "daniel.nguyen@example.edu", DateOfBirth: "2003-11-02", CourseCode: "BCY",
			Enrolments: []Enrolment{
				{"CSP1150", 2025, 1, mark(52)}, {"MAT1104", 2025, 1, mark(45)}, {"CSI2101", 2025, 2, mark(68)}, {"MAT1104", 2026, 1, mark(61)},
			}},
		{ID: "10000003", FirstName: "Mei", LastName: "Tanaka", Email: "mei.tanaka@example.edu", DateOfBirth: "2005-07-21", CourseCode: "BIT",
			Enrolments: []Enrolment{{"CSP1150", 2026, 2, nil}, {"CSI1101", 2026, 2, nil}}},
	}
	for _, s := range demo {
		if err := repo.Add(s); err != nil {
			return err
		}
		if err := users.Create(s.ID, "Student#2026", RoleStudent, s.ID); err != nil {
			return err
		}
	}
	return users.Create("admin", "Admin#2026", RoleAdmin, "")
}
