package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const passwordSessionPrefix = "layercache-password-v1\n"
const passwordCost = 12
const maximumPasswordFileBytes = 256 << 10

type PasswordAccount struct {
	Email              string `json:"email"`
	User               User   `json:"user"`
	PasswordHash       string `json:"passwordHash"`
	MustChangePassword bool   `json:"mustChangePassword"`
}
type passwordFile struct {
	Accounts []PasswordAccount `json:"accounts"`
}

// passwordStore supports one active Portal writer per account file. SQL
// session storage does not serialize file rotations across multiple processes.
type passwordStore struct {
	path     string
	owners   map[User]bool
	mu       sync.Mutex
	dummy    []byte
	limitsMu sync.Mutex
	limits   map[string]passwordWindow
	global   passwordWindow
	work     chan struct{}
}
type passwordWindow struct {
	started time.Time
	count   int
}

func canonicalPasswordEmail(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	address, err := mail.ParseAddress(value)
	return value, err == nil && address.Address == value && len(value) <= 254 && !strings.ContainsAny(value, "\r\n\x00")
}
func validPassword(value string) bool {
	return len(value) >= 12 && len(value) <= 72 && !strings.ContainsRune(value, '\x00')
}
func newPasswordStore(path string, projects []OperatorProject) (*passwordStore, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("password account file path must be absolute")
	}
	store := &passwordStore{path: path, owners: map[User]bool{}, limits: map[string]passwordWindow{}, work: make(chan struct{}, 4)}
	for _, project := range projects {
		store.owners[project.Owner] = true
	}
	if _, err := store.read(); err != nil {
		return nil, err
	}
	var err error
	store.dummy, err = bcrypt.GenerateFromPassword([]byte(randomID("")), passwordCost)
	return store, err
}
func (s *passwordStore) read() (passwordFile, error) {
	data, err := readPasswordAccountsFile(s.path)
	if err != nil {
		return passwordFile{}, errors.New("cannot read protected password account file")
	}
	var file passwordFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&file) != nil || decoder.Decode(&struct{}{}) != io.EOF || len(file.Accounts) == 0 || len(file.Accounts) > 128 {
		return passwordFile{}, errors.New("invalid password account file")
	}
	emails, users := map[string]bool{}, map[string]bool{}
	for _, account := range file.Accounts {
		email, valid := canonicalPasswordEmail(account.Email)
		cost, hashErr := bcrypt.Cost([]byte(account.PasswordHash))
		if !valid || email != account.Email || !s.owners[account.User] || emails[email] || users[account.User.ID] || hashErr != nil || cost != passwordCost {
			return passwordFile{}, errors.New("password accounts require distinct canonical emails, cost-12 bcrypt hashes and existing operator owners")
		}
		emails[email], users[account.User.ID] = true, true
	}
	return file, nil
}
func (s *passwordStore) account(email string) (PasswordAccount, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.read()
	if err != nil {
		return PasswordAccount{}, false
	}
	for _, account := range file.Accounts {
		if account.Email == email {
			return account, true
		}
	}
	return PasswordAccount{}, false
}
func (s *passwordStore) allow(r *http.Request, email string) bool {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	now := time.Now()
	window := 15 * time.Minute
	if s.global.started.IsZero() || now.Sub(s.global.started) >= window {
		s.global = passwordWindow{started: now}
	}
	s.global.count++
	if s.global.count > 120 {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	keys := []string{"ip:" + tokenHash(host), "email:" + tokenHash(email)}
	for key, entry := range s.limits {
		if now.Sub(entry.started) >= window {
			delete(s.limits, key)
		}
	}
	newKeys := 0
	for _, key := range keys {
		if _, ok := s.limits[key]; !ok {
			newKeys++
		}
	}
	if len(s.limits)+newKeys > 2048 {
		return false
	}
	allowed := true
	for _, key := range keys {
		entry := s.limits[key]
		if entry.started.IsZero() {
			entry.started = now
		}
		entry.count++
		s.limits[key] = entry
		maximum := 20
		if strings.HasPrefix(key, "email:") {
			maximum = 8
		}
		if entry.count > maximum {
			allowed = false
		}
	}
	return allowed
}
func (s *passwordStore) acquire(w http.ResponseWriter, r *http.Request, email string) bool {
	if !s.allow(r, email) {
		jsonResponse(w, 429, map[string]string{"error": "too many sign-in attempts; try again later"})
		return false
	}
	select {
	case s.work <- struct{}{}:
		return true
	default:
		jsonResponse(w, 429, map[string]string{"error": "too many sign-in attempts; try again later"})
		return false
	}
}
func readPasswordJSON(w http.ResponseWriter, r *http.Request, target any) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return ErrInvalid
	}
	// These credential requests are flat objects of strings. Reject repeated
	// keys as well as unknown fields, rather than choosing one password value.
	check := json.NewDecoder(bytes.NewReader(data))
	opening, err := check.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for check.More() {
		key, err := check.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return ErrInvalid
		}
		seen[name] = true
		var value string
		if check.Decode(&value) != nil {
			return ErrInvalid
		}
	}
	if _, err := check.Token(); err != nil {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (p *Portal) passwordLogin(w http.ResponseWriter, r *http.Request) {
	if p.passwords == nil {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Origin") != p.cfg.Origin {
		fail(w, ErrDenied)
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if readPasswordJSON(w, r, &input) != nil {
		fail(w, ErrInvalid)
		return
	}
	email, _ := canonicalPasswordEmail(input.Email)
	if !p.passwords.acquire(w, r, email) {
		return
	}
	defer func() { <-p.passwords.work }()
	account, found := p.passwords.account(email)
	hash := p.passwords.dummy
	if found {
		hash = []byte(account.PasswordHash)
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(input.Password))
	if !found || !validPassword(input.Password) || err != nil {
		jsonResponse(w, 401, map[string]string{"error": "email or password is incorrect"})
		return
	}
	if err = p.passwordSession(w, r, account); err != nil {
		fail(w, err)
		return
	}
	jsonResponse(w, 200, map[string]bool{"ok": true})
}
func (p *Portal) passwordSession(w http.ResponseWriter, r *http.Request, account PasswordAccount) error {
	if err := p.store.UpsertUser(r.Context(), account.User); err != nil {
		return err
	}
	token := randomID("")
	session := Session{User: account.User, CSRF: randomID(""), EncryptedToken: p.encrypt(passwordSessionPrefix + account.Email + "\n" + tokenHash(account.PasswordHash)), ExpiresAt: time.Now().Add(12 * time.Hour).Unix()}
	if err := p.store.SaveSession(r.Context(), token, session); err != nil {
		return err
	}
	p.cookie(w, "session", token, int(time.Until(time.Unix(session.ExpiresAt, 0))/time.Second))
	return nil
}
func (p *Portal) authenticatePasswordSession(session *Session) error {
	session.AuthMethod = "github"
	token, err := p.decrypt(session.EncryptedToken)
	if err != nil {
		return ErrDenied
	}
	if !strings.HasPrefix(token, passwordSessionPrefix) {
		return nil
	}
	session.AuthMethod = "password"
	if p.passwords == nil {
		return ErrDenied
	}
	parts := strings.Split(strings.TrimPrefix(token, passwordSessionPrefix), "\n")
	if len(parts) != 2 {
		return ErrDenied
	}
	account, ok := p.passwords.account(parts[0])
	if !ok || account.User != session.User || parts[1] != tokenHash(account.PasswordHash) {
		return ErrDenied
	}
	session.PasswordChangeRequired = account.MustChangePassword
	session.PasswordEmail = account.Email
	return nil
}
func (p *Portal) changePassword(w http.ResponseWriter, r *http.Request, session Session) {
	if session.AuthMethod != "password" || p.passwords == nil {
		fail(w, ErrDenied)
		return
	}
	var input struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if readPasswordJSON(w, r, &input) != nil || !validPassword(input.New) || !validPassword(input.Current) || input.Current == input.New {
		fail(w, ErrInvalid)
		return
	}
	if !p.passwords.acquire(w, r, session.PasswordEmail) {
		return
	}
	defer func() { <-p.passwords.work }()
	s := p.passwords
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := s.read()
	if err != nil {
		fail(w, err)
		return
	}
	for i, account := range file.Accounts {
		if account.Email != session.PasswordEmail || account.User != session.User {
			continue
		}
		if bcrypt.CompareHashAndPassword([]byte(account.PasswordHash), []byte(input.Current)) != nil {
			fail(w, ErrDenied)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(input.New), passwordCost)
		if err != nil {
			fail(w, err)
			return
		}
		account.PasswordHash, account.MustChangePassword = string(hash), false
		file.Accounts[i] = account
		data, err := json.MarshalIndent(file, "", "  ")
		if err != nil {
			fail(w, err)
			return
		}
		if err = writePasswordAccountsFile(s.path, append(data, '\n')); err != nil {
			fail(w, errors.New("cannot persist password change"))
			return
		}
		// The session marker is bound to the old hash, so all old password sessions
		// are invalid immediately, even if SQL cleanup or response delivery fails.
		if err = p.passwordSession(w, r, account); err != nil {
			fail(w, err)
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	fail(w, ErrDenied)
}
func passwordProviderAllowed(w http.ResponseWriter, session Session) bool {
	if session.AuthMethod == "password" {
		jsonResponse(w, 403, map[string]string{"error": "GitHub sign-in is required to manage teams, projects and invitations"})
		return false
	}
	return true
}
