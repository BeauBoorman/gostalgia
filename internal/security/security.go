// Package security defines the environment's identity and capability
// primitives: users and permission tokens checked at IPC handler
// boundaries. It is deliberately tiny. It provides logical isolation
// only; see docs/security.md for exactly what is and is not protected.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"gostalgia/sdk"
)

// User is an environment-level identity. Users exist independently of the
// host OS's user accounts.
type User struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PrincipalKind distinguishes operator authority from application authority.
type PrincipalKind string

const (
	PrincipalKindOperator PrincipalKind = "operator"
	PrincipalKindApp      PrincipalKind = "app"
)

// Principal represents the authenticated caller identity.
type Principal struct {
	Kind      PrincipalKind `json:"kind"`
	AppID     string        `json:"app_id,omitempty"`
	ProcessID int32         `json:"process_id,omitempty"`
	SessionID string        `json:"session_id,omitempty"`
	User      User          `json:"user,omitempty"`
}

func (p Principal) IsOperator() bool { return p.Kind == PrincipalKindOperator }
func (p Principal) IsApp() bool      { return p.Kind == PrincipalKindApp }

// OperatorPrincipal creates an operator principal.
func OperatorPrincipal(user User) Principal {
	return Principal{
		Kind: PrincipalKindOperator,
		User: user,
	}
}

// AppPrincipal creates a launch-bound application principal.
func AppPrincipal(appID string, procID int32, sessionID string, user User) Principal {
	return Principal{
		Kind:      PrincipalKindApp,
		AppID:     appID,
		ProcessID: procID,
		SessionID: sessionID,
		User:      user,
	}
}

// Credential holds an active or revoked credential and its bound identity.
type Credential struct {
	Token        string        `json:"token"`
	Principal    Principal     `json:"principal"`
	Capabilities *Capabilities `json:"capabilities"`
	CreatedAt    time.Time     `json:"created_at"`
	Revoked      bool          `json:"revoked"`
	RevokedAt    time.Time     `json:"revoked_at,omitempty"`
}

// TokenStore issues, validates, and revokes credentials for operators and
// applications. It enforces server-side identity validation, binding tokens to
// specific approved principals and grants rather than trusting client claims.
// Safe for concurrent use.
type TokenStore struct {
	mu          sync.RWMutex
	credentials map[string]*Credential
	byApp       map[string][]string // appID -> []token
	byProc      map[int32][]string  // procID -> []token
}

func NewTokenStore() *TokenStore {
	return &TokenStore{
		credentials: make(map[string]*Credential),
		byApp:       make(map[string][]string),
		byProc:      make(map[int32][]string),
	}
}

// RegisterOperator registers a static operator token with admin capabilities.
func (s *TokenStore) RegisterOperator(token string, user User) error {
	if token == "" {
		return errors.New("security: operator token is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.credentials[token]; exists {
		return errors.New("security: token already registered")
	}
	s.credentials[token] = &Credential{
		Token:        token,
		Principal:    OperatorPrincipal(user),
		Capabilities: AdminCapabilities(),
		CreatedAt:    time.Now(),
	}
	return nil
}

// SetOperatorUser updates the user for all operator credentials.
func (s *TokenStore) SetOperatorUser(user User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cred := range s.credentials {
		if cred.Principal.IsOperator() {
			cred.Principal.User = user
		}
	}
}

// IssueAppToken generates a launch-bound credential for an application with
// its approved capability grants.
func (s *TokenStore) IssueAppToken(appID string, procID int32, sessionID string, user User, caps ...string) (string, error) {
	if appID == "" {
		return "", errors.New("security: app ID is required")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("security: generate app token: %w", err)
	}
	token := hex.EncodeToString(b)

	s.mu.Lock()
	defer s.mu.Unlock()
	cred := &Credential{
		Token:        token,
		Principal:    AppPrincipal(appID, procID, sessionID, user),
		Capabilities: NewCapabilities(caps...),
		CreatedAt:    time.Now(),
	}
	s.credentials[token] = cred
	s.byApp[appID] = append(s.byApp[appID], token)
	if procID != 0 {
		s.byProc[procID] = append(s.byProc[procID], token)
	}
	return token, nil
}

// BindProcess associates an issued app token with a process ID once launched.
func (s *TokenStore) BindProcess(token string, procID int32) {
	if procID == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cred, ok := s.credentials[token]; ok {
		cred.Principal.ProcessID = procID
		s.byProc[procID] = append(s.byProc[procID], token)
	}
}

// Authenticate verifies the token on connection handshake.
func (s *TokenStore) Authenticate(token string) (Principal, *Capabilities, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for tok, cred := range s.credentials {
		if subtle.ConstantTimeCompare([]byte(token), []byte(tok)) == 1 {
			if cred.Revoked {
				return Principal{}, nil, errors.New("unauthorized: token revoked")
			}
			return cred.Principal, NewCapabilities(cred.Capabilities.List()...), nil
		}
	}
	return Principal{}, nil, errors.New("unauthorized: bad token")
}

// Validate verifies whether an already-authenticated token remains active and unrevoked.
func (s *TokenStore) Validate(token string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for tok, cred := range s.credentials {
		if subtle.ConstantTimeCompare([]byte(token), []byte(tok)) == 1 {
			if cred.Revoked {
				return errors.New("unauthorized: credential revoked")
			}
			return nil
		}
	}
	return errors.New("unauthorized: bad token")
}

// Revoke invalidates a specific token immediately.
func (s *TokenStore) Revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cred, ok := s.credentials[token]; ok && !cred.Revoked {
		cred.Revoked = true
		cred.RevokedAt = time.Now()
	}
}

// RevokeApp invalidates all tokens issued for an application.
func (s *TokenStore) RevokeApp(appID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, tok := range s.byApp[appID] {
		if cred, ok := s.credentials[tok]; ok && !cred.Revoked {
			cred.Revoked = true
			cred.RevokedAt = now
		}
	}
}

// RevokeProcess invalidates all tokens associated with a process ID.
func (s *TokenStore) RevokeProcess(procID int32) {
	if procID == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, tok := range s.byProc[procID] {
		if cred, ok := s.credentials[tok]; ok && !cred.Revoked {
			cred.Revoked = true
			cred.RevokedAt = now
		}
	}
}

// Lookup returns a copy of the credential metadata for diagnostics.
func (s *TokenStore) Lookup(token string) (Credential, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for tok, cred := range s.credentials {
		if subtle.ConstantTimeCompare([]byte(token), []byte(tok)) == 1 {
			return Credential{
				Token:        tok,
				Principal:    cred.Principal,
				Capabilities: NewCapabilities(cred.Capabilities.List()...),
				CreatedAt:    cred.CreatedAt,
				Revoked:      cred.Revoked,
				RevokedAt:    cred.RevokedAt,
			}, true
		}
	}
	return Credential{}, false
}

// Well-known capabilities. Applications declare the ones they need in
// their manifest; the runtime grants them to the application's call
// context, and system services check them before acting.
const (
	CapIPC            = sdk.CapIPC
	CapFileRead       = sdk.CapFileRead
	CapFileWrite      = sdk.CapFileWrite
	CapProcList       = sdk.CapProcList
	CapProcStop       = sdk.CapProcStop
	CapAppList        = sdk.CapAppList
	CapAppLaunch      = sdk.CapAppLaunch
	CapShutdown       = sdk.CapShutdown
	CapConfigRead     = sdk.CapConfigRead
	CapConfigWrite    = sdk.CapConfigWrite
	CapClipboardRead  = sdk.CapClipboardRead
	CapClipboardWrite = sdk.CapClipboardWrite
	CapHostFSRead     = sdk.CapHostFSRead
	CapHostFSWrite    = sdk.CapHostFSWrite
	CapNetEgress      = sdk.CapNetEgress
	CapSessionRead    = sdk.CapSessionRead
	CapSessionWrite   = sdk.CapSessionWrite
	CapProfileRead    = sdk.CapProfileRead
	CapProfileWrite   = sdk.CapProfileWrite
	CapAdmin          = "admin"
)

// Capabilities is a concurrency-safe permission set.
type Capabilities struct {
	mu   sync.RWMutex
	caps map[string]bool
}

func NewCapabilities(capabilities ...string) *Capabilities {
	c := &Capabilities{caps: make(map[string]bool, len(capabilities))}
	for _, name := range capabilities {
		c.caps[name] = true
	}
	return c
}

func (c *Capabilities) Has(capability string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps[capability]
}

func (c *Capabilities) Grant(capabilities ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range capabilities {
		c.caps[name] = true
	}
}

// List returns the granted capabilities, sorted.
func (c *Capabilities) List() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.caps))
	for name := range c.caps {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AdminCapabilities returns the full capability set granted to trusted
// local control clients (gctl) and to the runtime's own in-process
// calls. Applications never receive this set; they get what their
// manifest declares.
func AdminCapabilities() *Capabilities {
	return NewCapabilities(
		CapIPC, CapFileRead, CapFileWrite,
		CapProcList, CapProcStop,
		CapAppList, CapAppLaunch,
		CapShutdown,
		CapConfigRead, CapConfigWrite,
		CapClipboardRead, CapClipboardWrite,
		CapHostFSRead, CapHostFSWrite,
		CapNetEgress,
		CapSessionRead, CapSessionWrite,
		CapProfileRead, CapProfileWrite,
		CapAdmin,
	)
}
