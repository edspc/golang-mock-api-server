package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Routes registers the sign-in endpoints under prefix (e.g. "/api/"). They are
// the only control routes reachable without a session — Guard lets them past.
func (a *Auth) Routes(mux *http.ServeMux, prefix string) {
	if !a.Enabled() {
		// Even with sign-in off the console asks whether it is needed.
		mux.HandleFunc("GET "+prefix+"auth/status", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "authenticated": true})
		})
		return
	}
	a.callbackPath = prefix + "auth/callback"

	mux.HandleFunc("GET "+prefix+"auth/status", func(w http.ResponseWriter, r *http.Request) {
		s, ok := a.session(r)
		body := map[string]any{"enabled": true, "authenticated": ok}
		if ok {
			body["email"] = s.Email
		}
		writeJSON(w, http.StatusOK, body)
	})

	mux.HandleFunc("GET "+prefix+"auth/login", func(w http.ResponseWriter, r *http.Request) {
		state, err := randomString()
		if err != nil {
			a.log.Error("generate oauth state", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot start sign-in"})
			return
		}
		// The state is signed into a short-lived cookie and compared on the
		// way back, so a forged callback cannot sign anyone in.
		a.setCookie(w, stateCookie, a.sign(state), stateTTL)

		q := url.Values{
			"client_id":     {a.cfg.ClientID},
			"redirect_uri":  {a.redirectURI()},
			"response_type": {"code"},
			"scope":         {"openid email"},
			"state":         {state},
			"prompt":        {"select_account"},
		}
		http.Redirect(w, r, authEndpoint+"?"+q.Encode(), http.StatusFound)
	})

	mux.HandleFunc("GET "+prefix+"auth/callback", func(w http.ResponseWriter, r *http.Request) {
		a.handleCallback(w, r)
	})

	mux.HandleFunc("POST "+prefix+"auth/logout", func(w http.ResponseWriter, r *http.Request) {
		a.clearCookie(w, sessionCookie)
		writeJSON(w, http.StatusOK, map[string]string{"status": "signed out"})
	})
}

func (a *Auth) handleCallback(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, msg string, args ...any) {
		a.log.Warn("sign-in failed", args...)
		a.clearCookie(w, stateCookie)
		writeJSON(w, status, map[string]string{"error": msg})
	}

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		fail(http.StatusUnauthorized, "sign-in was declined: "+errParam, "reason", errParam)
		return
	}

	cookie, err := r.Cookie(stateCookie)
	if err != nil {
		fail(http.StatusBadRequest, "sign-in expired, please try again", "reason", "no state cookie")
		return
	}
	want, ok := a.verify(cookie.Value)
	if !ok || want == "" || want != r.URL.Query().Get("state") {
		fail(http.StatusBadRequest, "sign-in state did not match, please try again", "reason", "state mismatch")
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		fail(http.StatusBadRequest, "sign-in returned no code", "reason", "missing code")
		return
	}

	email, err := a.exchange(r.Context(), code)
	if err != nil {
		fail(http.StatusBadGateway, "could not complete sign-in", "error", err)
		return
	}
	if !a.Allows(email) {
		// Deliberately specific: the person needs to know which account was
		// refused, and knowing it is not on the list reveals nothing secret.
		fail(http.StatusForbidden, email+" is not allowed to manage endpoints", "email", email)
		return
	}

	a.clearCookie(w, stateCookie)
	expires := time.Now().Add(sessionTTL)
	a.setCookie(w, sessionCookie, a.sign(email+"|"+strconv.FormatInt(expires.Unix(), 10)), sessionTTL)
	a.log.Info("signed in", "email", email)
	// Written by hand rather than with http.Redirect, which resolves a relative
	// target against the request path and would hand the browser an absolute
	// "/" again — see consolePath.
	w.Header().Set("Location", a.consolePath())
	w.WriteHeader(http.StatusFound)
}

// consolePath is where to send the browser once it is signed in: the console,
// expressed *relative* to the callback route. A literal "/" is wrong whenever
// the app is mounted under a sub-path — the browser is then at
// /mockapi/api/auth/callback while the server only ever sees
// /api/auth/callback, and has no way to learn the difference. The browser
// resolves the relative form against the URL it actually asked for, so it
// lands on the console either way.
func (a *Auth) consolePath() string {
	up := strings.Count(strings.Trim(a.callbackPath, "/"), "/")
	return strings.Repeat("../", up)
}

// exchange trades the authorization code for the signed-in address.
func (a *Auth) exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"code":          {code},
		"client_id":     {a.cfg.ClientID},
		"client_secret": {a.cfg.ClientSecret},
		"redirect_uri":  {a.redirectURI()},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := a.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token exchange: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange: google returned %s", res.Status)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&token); err != nil {
		return "", fmt.Errorf("decode token: %w", err)
	}
	if token.AccessToken == "" {
		return "", fmt.Errorf("token exchange: no access token in the response")
	}

	// The userinfo endpoint is used rather than parsing the id_token: the
	// answer arrives straight from Google over TLS, so there is no JWT
	// signature to verify and no key set to fetch.
	info, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoEndpoint, nil)
	if err != nil {
		return "", err
	}
	info.Header.Set("Authorization", "Bearer "+token.AccessToken)
	infoRes, err := a.client.Do(info)
	if err != nil {
		return "", fmt.Errorf("userinfo: %w", err)
	}
	defer infoRes.Body.Close()
	if infoRes.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo: google returned %s", infoRes.Status)
	}
	var profile struct {
		Email    string `json:"email"`
		Verified bool   `json:"email_verified"`
	}
	if err := json.NewDecoder(infoRes.Body).Decode(&profile); err != nil {
		return "", fmt.Errorf("decode userinfo: %w", err)
	}
	if profile.Email == "" {
		return "", fmt.Errorf("userinfo: no email in the response")
	}
	if !profile.Verified {
		return "", fmt.Errorf("userinfo: %s is not a verified address", profile.Email)
	}
	return strings.ToLower(profile.Email), nil
}

// Guard requires a session for control routes other than sign-in itself.
// Callback traffic never reaches it.
func (a *Auth) Guard(prefix string, next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}
	authPrefix := prefix + "auth/"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, authPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := a.session(r); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "sign in to manage endpoints",
				"login": prefix + "auth/login",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
