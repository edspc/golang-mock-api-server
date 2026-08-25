// Package auth gates the control API behind Google sign-in.
//
// It protects only the management surface. Callback URLs stay open: a third
// party posting to /cb/{id} has no way to sign in, and requiring it would
// defeat the point of the service.
//
// Without a client id and secret the whole package stays dormant and every
// request is treated as authorized, which is the default the service ships
// with.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie = "mockapi_session"
	stateCookie   = "mockapi_oauth_state"
	sessionTTL    = 12 * time.Hour
	stateTTL      = 10 * time.Minute

	authEndpoint     = "https://accounts.google.com/o/oauth2/v2/auth"
	tokenEndpoint    = "https://oauth2.googleapis.com/token"
	userinfoEndpoint = "https://openidconnect.googleapis.com/v1/userinfo"
)

// Config is what the environment supplies.
type Config struct {
	ClientID     string
	ClientSecret string
	// BaseURL is the externally visible origin, used to build the redirect
	// URI that must match the one registered with Google.
	BaseURL string
	// AllowedEmails and AllowedDomain decide who may manage endpoints. At
	// least one must be set: without a restriction every Google account on
	// earth would qualify, which is not authentication at all.
	AllowedEmails []string
	AllowedDomain string
}

// Configured reports whether sign-in was asked for at all.
func (c Config) Configured() bool { return c.ClientID != "" || c.ClientSecret != "" }

// Auth guards the control API. A nil *Auth is a valid, disabled guard.
type Auth struct {
	cfg    Config
	key    []byte
	log    *slog.Logger
	client *http.Client
	secure bool

	// callbackPath is filled in by Routes, so this package need not know the
	// control prefix the server chose.
	callbackPath string
}

// New validates the configuration and returns a guard. It returns a nil Auth
// when sign-in is not configured, which every method handles.
func New(cfg Config, log *slog.Logger) (*Auth, error) {
	if !cfg.Configured() {
		return nil, nil
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("auth: both a client id and a client secret are required")
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("auth: a base URL is required to build the OAuth redirect URI")
	}
	if len(cfg.AllowedEmails) == 0 && cfg.AllowedDomain == "" {
		return nil, errors.New("auth: refusing to enable sign-in without an allowed email list or domain — " +
			"otherwise any Google account could manage your endpoints")
	}

	// A per-process key means sessions end with the process. Endpoints may
	// outlive it in SQLite, but a signing key on disk is a liability that
	// buys only the convenience of staying signed in across a restart.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("auth: generate session key: %w", err)
	}

	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &Auth{
		cfg:    cfg,
		key:    key,
		log:    log,
		client: &http.Client{Timeout: 15 * time.Second},
		secure: strings.HasPrefix(cfg.BaseURL, "https://"),
	}, nil
}

// Enabled reports whether requests are actually being checked.
func (a *Auth) Enabled() bool { return a != nil }

// Allows reports whether an email may manage endpoints.
func (a *Auth) Allows(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, allowed := range a.cfg.AllowedEmails {
		if strings.EqualFold(strings.TrimSpace(allowed), email) {
			return true
		}
	}
	if a.cfg.AllowedDomain != "" {
		domain := strings.ToLower(strings.TrimPrefix(a.cfg.AllowedDomain, "@"))
		if _, got, ok := strings.Cut(email, "@"); ok && got == domain {
			return true
		}
	}
	return false
}

func (a *Auth) redirectURI() string {
	return strings.TrimSuffix(a.cfg.BaseURL, "/") + a.callbackPath
}

/* ---------- sessions ---------- */

// sign returns payload with its HMAC appended, both base64url encoded.
func (a *Auth) sign(payload string) string {
	mac := hmac.New(sha256.New, a.key)
	mac.Write([]byte(payload))
	enc := base64.RawURLEncoding
	return enc.EncodeToString([]byte(payload)) + "." + enc.EncodeToString(mac.Sum(nil))
}

// verify returns the payload of a value produced by sign.
func (a *Auth) verify(value string) (string, bool) {
	encoded, sig, ok := strings.Cut(value, ".")
	if !ok {
		return "", false
	}
	enc := base64.RawURLEncoding
	payload, err := enc.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	want, err := enc.DecodeString(sig)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, a.key)
	mac.Write(payload)
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return "", false
	}
	return string(payload), true
}

// Session is the signed-in user, if any.
type Session struct {
	Email   string
	Expires time.Time
}

// session reads and validates the session cookie.
func (a *Auth) session(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return Session{}, false
	}
	payload, ok := a.verify(c.Value)
	if !ok {
		return Session{}, false
	}
	email, expires, ok := strings.Cut(payload, "|")
	if !ok {
		return Session{}, false
	}
	unix, err := strconv.ParseInt(expires, 10, 64)
	if err != nil {
		return Session{}, false
	}
	if time.Now().After(time.Unix(unix, 0)) {
		return Session{}, false
	}
	// The allowlist is re-checked on every request, so revoking access takes
	// effect at the next request rather than at the next sign-in.
	if !a.Allows(email) {
		return Session{}, false
	}
	return Session{Email: email, Expires: time.Unix(unix, 0)}, true
}

func (a *Auth) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func (a *Auth) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// randomString returns an unguessable value for the OAuth state parameter.
func randomString() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
