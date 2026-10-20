// Rafei Ahamed - 10696700

// auth.go - users, password hashing and sessions (the admin / student split).
package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrBadLogin   = errors.New("incorrect username or password")
	ErrUserExists = errors.New("user already exists")
)

// pbkdf2Iterations follows the OWASP guidance for PBKDF2-HMAC-SHA256. It is a
// variable (not a constant) only so unit tests can lower it to run quickly.
var pbkdf2Iterations = 600_000

// User is a login account. Students are linked to their record by StudentID.
type User struct {
	Username   string `json:"username"`
	Role       Role   `json:"role"`
	StudentID  string `json:"studentId,omitempty"`
	Salt       string `json:"salt"`
	Hash       string `json:"hash"`
	Iterations int    `json:"iterations"`
}

// derive hashes password with the given salt using PBKDF2 from the standard
// library (crypto/pbkdf2 is generic over the hash type, added in Go 1.24).
func derive(password string, salt []byte, iterations int) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, 32)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// UserStore holds accounts in memory and mirrors them to a JSON file.
type UserStore struct {
	path  string
	mu    sync.RWMutex
	users map[string]User
}

// NewUserStore loads path if it exists. An empty path means in-memory only.
func NewUserStore(path string) (*UserStore, error) {
	st := &UserStore{path: path, users: map[string]User{}}
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
	var list []User
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, u := range list {
		st.users[u.Username] = u
	}
	return st, nil
}

// Count returns the number of accounts.
func (st *UserStore) Count() int {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return len(st.users)
}

// Create hashes the password with a fresh random salt and stores the account.
func (st *UserStore) Create(username, password string, role Role, studentID string) error {
	if !role.Valid() {
		return fmt.Errorf("invalid role %v", role)
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	hash, err := derive(password, salt, pbkdf2Iterations)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, exists := st.users[username]; exists {
		return fmt.Errorf("%w: %s", ErrUserExists, username)
	}
	next := make(map[string]User, len(st.users)+1)
	for k, v := range st.users {
		next[k] = v
	}
	next[username] = User{
		Username: username, Role: role, StudentID: studentID,
		Salt: base64.StdEncoding.EncodeToString(salt), Hash: hash, Iterations: pbkdf2Iterations,
	}
	return st.commit(next)
}

// Remove deletes an account (no error if it does not exist).
func (st *UserStore) Remove(username string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if _, ok := st.users[username]; !ok {
		return nil
	}
	next := make(map[string]User, len(st.users))
	for k, v := range st.users {
		if k != username {
			next[k] = v
		}
	}
	return st.commit(next)
}

func (st *UserStore) commit(next map[string]User) error {
	if st.path != "" {
		list := make([]User, 0, len(next))
		for _, u := range next {
			list = append(list, u)
		}
		slices.SortFunc(list, func(a, b User) int { return strings.Compare(a.Username, b.Username) })
		if err := writeJSONAtomic(st.path, list); err != nil {
			return err
		}
	}
	st.users = next
	return nil
}

// SetPassword changes a user's password after checking the current one. The new
// password gets its own fresh random salt. Rule problems come back as
// ValidationErrors; a wrong current password is ErrBadLogin.
func (st *UserStore) SetPassword(username, current, newPassword string) error {
	if _, err := st.Authenticate(username, current); err != nil {
		return err
	}
	if n := utf8.RuneCountInString(newPassword); n < 8 || n > 128 {
		return ValidationErrors{"newPassword": "New password must be 8 to 128 characters long."}
	}
	if newPassword == current {
		return ValidationErrors{"newPassword": "New password must be different from the current one."}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}
	hash, err := derive(newPassword, salt, pbkdf2Iterations)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	u, ok := st.users[username]
	if !ok {
		return ErrBadLogin
	}
	u.Salt, u.Hash, u.Iterations = base64.StdEncoding.EncodeToString(salt), hash, pbkdf2Iterations
	next := maps.Clone(st.users)
	next[username] = u
	return st.commit(next)
}

// Authenticate checks a username and password. The same ErrBadLogin is returned
// for an unknown user and a wrong password, and a dummy hash is still computed
// for unknown users so response time does not reveal which usernames exist.
func (st *UserStore) Authenticate(username, password string) (User, error) {
	st.mu.RLock()
	u, ok := st.users[username]
	st.mu.RUnlock()
	if !ok {
		_, _ = derive(password, make([]byte, 16), pbkdf2Iterations)
		return User{}, ErrBadLogin
	}
	salt, err := base64.StdEncoding.DecodeString(u.Salt)
	if err != nil {
		return User{}, fmt.Errorf("corrupt salt for %s: %w", username, err)
	}
	got, err := derive(password, salt, u.Iterations)
	if err != nil {
		return User{}, err
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(u.Hash)) != 1 {
		return User{}, ErrBadLogin
	}
	return u, nil
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// flash is a one-time message shown on the next page the user loads.
type flash struct {
	Kind string // "ok" or "error"
	Msg  string
}

type session struct {
	User    User
	CSRF    string // token every POST form must echo back
	Expires time.Time
	Flash   *flash
}

// SessionManager keeps sessions in memory. It is shared by every request
// goroutine, so every method takes the mutex.
type SessionManager struct {
	mu    sync.Mutex
	items map[string]session
	ttl   time.Duration
}

func NewSessionManager(ttl time.Duration) *SessionManager {
	return &SessionManager{items: map[string]session{}, ttl: ttl}
}

// Create starts a session and returns its secret cookie token.
// rand.Text (Go 1.24) returns a cryptographically random base32 string.
func (m *SessionManager) Create(u User) (string, session) {
	token := rand.Text()
	s := session{User: u, CSRF: rand.Text(), Expires: time.Now().Add(m.ttl)}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[token] = s
	return token, s
}

// Lookup returns the live session for token, deleting it if it has expired.
func (m *SessionManager) Lookup(token string) (session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.items[token]
	if ok && time.Now().After(s.Expires) {
		delete(m.items, token)
		return session{}, false
	}
	return s, ok
}

// Destroy ends a session (logout).
func (m *SessionManager) Destroy(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, token)
}

// SetFlash stores a message to show on the next page view.
func (m *SessionManager) SetFlash(token string, f flash) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.items[token]; ok {
		s.Flash = &f
		m.items[token] = s
	}
}

// TakeFlash returns and clears the pending message, if any.
func (m *SessionManager) TakeFlash(token string) *flash {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.items[token]
	if !ok || s.Flash == nil {
		return nil
	}
	f := s.Flash
	s.Flash = nil
	m.items[token] = s
	return f
}
