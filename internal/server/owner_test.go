package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/edspc/golang-mock-api-server/internal/auth"
	"github.com/edspc/golang-mock-api-server/internal/endpoint"
)

// account is a signed-in caller: the same server, seen through one session.
type account struct {
	srv    *Server
	cookie *http.Cookie
}

// openSignIn builds a server whose control API needs a session but takes any
// Google account — the setup this file exists to pin down.
func openSignIn(t *testing.T, registry *endpoint.Registry) (*Server, *auth.Auth) {
	t.Helper()
	guard, err := auth.New(auth.Config{
		ClientID:     "id",
		ClientSecret: "secret",
		BaseURL:      "https://mock.example.com",
	}, nil)
	if err != nil {
		t.Fatalf("auth.New() error = %v", err)
	}
	srv, err := New(Options{Endpoints: registry, Auth: guard})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return srv, guard
}

func signIn(t *testing.T, srv *Server, guard *auth.Auth, email string) account {
	t.Helper()
	w := httptest.NewRecorder()
	guard.StartSession(w, email)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("StartSession set %d cookies, want 1", len(cookies))
	}
	return account{srv: srv, cookie: cookies[0]}
}

func (a account) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	r.AddCookie(a.cookie)
	w := httptest.NewRecorder()
	a.srv.ServeHTTP(w, r)
	return w
}

func (a account) create(t *testing.T, body string) endpointView {
	t.Helper()
	w := a.do(t, http.MethodPost, "/api/endpoints", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s, want 201", w.Code, w.Body)
	}
	var view endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func (a account) list(t *testing.T) []endpointView {
	t.Helper()
	w := a.do(t, http.MethodGet, "/api/endpoints", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", w.Code)
	}
	var views []endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	return views
}

// An endpoint belongs to the account that created it. Everyone else is told it
// does not exist — its URL is public anyway, so 403 would only confirm the id.
func TestEndpointsAreVisibleOnlyToTheirOwner(t *testing.T) {
	srv, guard := openSignIn(t, endpoint.NewRegistry(10))
	mine := signIn(t, srv, guard, "me@edspc.dev")
	theirs := signIn(t, srv, guard, "someone@else.com")

	view := mine.create(t, `{"name":"private","spec":{"rules":[{"name":"ack","response":{"status":202}}]}}`)

	if got := mine.list(t); len(got) != 1 || got[0].ID != view.ID {
		t.Errorf("the owner sees %d endpoints, want their own one", len(got))
	}
	if got := theirs.list(t); len(got) != 0 {
		t.Errorf("another account sees %d endpoints, want none", len(got))
	}

	for _, tc := range []struct{ name, method, target, body string }{
		{"read", http.MethodGet, "/api/endpoints/" + view.ID, ""},
		{"requests", http.MethodGet, "/api/endpoints/" + view.ID + "/requests", ""},
		{"rename", http.MethodPut, "/api/endpoints/" + view.ID + "/name", `{"name":"stolen"}`},
		{"rewrite the spec", http.MethodPut, "/api/endpoints/" + view.ID + "/spec", `{"response":{"status":500}}`},
		{"clear the history", http.MethodPost, "/api/endpoints/" + view.ID + "/reset", ""},
		{"delete", http.MethodDelete, "/api/endpoints/" + view.ID, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := theirs.do(t, tc.method, tc.target, tc.body); w.Code != http.StatusNotFound {
				t.Errorf("status = %d, body = %s, want 404", w.Code, w.Body)
			}
		})
	}

	// None of that touched it.
	if w := mine.do(t, http.MethodGet, "/api/endpoints/"+view.ID, ""); !strings.Contains(w.Body.String(), `"name": "private"`) {
		t.Errorf("owner's endpoint = %s, want it untouched", w.Body)
	}
}

// Ownership guards the console, never the callback URL: a third party posting
// to /cb/{id} has no account and must not need one.
func TestCallbacksIgnoreOwnership(t *testing.T) {
	srv, guard := openSignIn(t, endpoint.NewRegistry(10))
	mine := signIn(t, srv, guard, "me@edspc.dev")
	view := mine.create(t, `{"spec":{"rules":[{"name":"ack","response":{"status":202}}]}}`)

	// No session at all, like a real caller.
	if w := do(t, srv, http.MethodPost, view.URL, `{"event":"paid"}`); w.Code != 202 {
		t.Errorf("anonymous callback status = %d, want the endpoint's 202", w.Code)
	}
	// And another account's session does not change that.
	theirs := signIn(t, srv, guard, "someone@else.com")
	if w := theirs.do(t, http.MethodPost, view.URL, ""); w.Code != 202 {
		t.Errorf("callback from another account = %d, want 202", w.Code)
	}

	if got := mine.list(t); len(got) != 1 || got[0].Received != 2 {
		t.Errorf("owner sees received = %+v, want both callbacks counted", got)
	}
}

// The count beside the console's logo is the caller's own, not the process's.
func TestHealthCountsOnlyYourEndpoints(t *testing.T) {
	srv, guard := openSignIn(t, endpoint.NewRegistry(10))
	mine := signIn(t, srv, guard, "me@edspc.dev")
	theirs := signIn(t, srv, guard, "someone@else.com")

	mine.create(t, "")
	mine.create(t, "")
	theirs.create(t, "")

	for _, tc := range []struct {
		who  account
		want string
	}{
		{mine, `"endpoints": 2`},
		{theirs, `"endpoints": 1`},
	} {
		w := tc.who.do(t, http.MethodGet, "/api/health", "")
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("health = %s, want %s", w.Body, tc.want)
		}
	}
}

// Endpoints created while sign-in was off belong to the anonymous owner. Turn
// sign-in on and they belong to nobody — they keep answering callbacks, but no
// account can see or change them.
func TestEndpointsCreatedBeforeSignInBelongToNobody(t *testing.T) {
	registry := endpoint.NewRegistry(10)

	// Before: no sign-in configured, so the console is open.
	open, err := New(Options{Endpoints: registry})
	if err != nil {
		t.Fatal(err)
	}
	view := createEndpoint(t, open, `{"name":"anonymous","spec":{"rules":[{"name":"ack","response":{"status":202}}]}}`)

	// After: the same registry, now behind sign-in.
	srv, guard := openSignIn(t, registry)
	for _, email := range []string{"me@edspc.dev", "someone@else.com"} {
		who := signIn(t, srv, guard, email)
		if got := who.list(t); len(got) != 0 {
			t.Errorf("%s sees %d endpoints, want none of the anonymous ones", email, len(got))
		}
		if w := who.do(t, http.MethodGet, "/api/endpoints/"+view.ID, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s reading the anonymous endpoint = %d, want 404", email, w.Code)
		}
		if w := who.do(t, http.MethodDelete, "/api/endpoints/"+view.ID, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s deleting the anonymous endpoint = %d, want 404", email, w.Code)
		}
	}

	// It is orphaned, not broken: the URL a third party holds still works.
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != 202 {
		t.Errorf("callback status = %d, want the endpoint still answering its 202", w.Code)
	}
}

// The anonymous owner is an owner like any other: with sign-in off, the
// console keeps showing what it always did.
func TestWithoutSignInEverythingIsShared(t *testing.T) {
	srv := newTestServer(t)
	first := createEndpoint(t, srv, `{"name":"one"}`)
	createEndpoint(t, srv, `{"name":"two"}`)

	w := do(t, srv, http.MethodGet, "/api/endpoints", "")
	var views []endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("listed %d endpoints, want both", len(views))
	}
	if w := do(t, srv, http.MethodGet, "/api/endpoints/"+first.ID, ""); w.Code != http.StatusOK {
		t.Errorf("reading an endpoint with sign-in off = %d, want 200", w.Code)
	}
}

// The broker is registry-wide, so the stream is where events get matched to
// the account watching. Without that, one account would learn that another is
// receiving callbacks.
func TestEventStreamOnlyCarriesYourOwnEvents(t *testing.T) {
	registry := endpoint.NewRegistry(10)
	srv, guard := openSignIn(t, registry)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	mine := signIn(t, srv, guard, "me@edspc.dev")
	theirs := signIn(t, srv, guard, "someone@else.com")

	es := openStreamAs(t, ts.URL, mine.cookie)

	// Another account's endpoint and its traffic must not show up.
	elsewhere := theirs.create(t, "")
	if w := do(t, srv, http.MethodPost, elsewhere.URL, ""); w.Code != http.StatusOK {
		t.Fatalf("callback status = %d", w.Code)
	}

	// Ours does, and it is the first thing the stream says.
	own := mine.create(t, "")
	if ev := es.next(); ev.Type != "created" || ev.Endpoint != own.ID {
		t.Fatalf("first event = %+v, want created for our own %s", ev, own.ID)
	}
}

func (a account) share(t *testing.T, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	return a.do(t, http.MethodPut, "/api/endpoints/"+id+"/share", body)
}

// Sharing hands over the same access the owner has — everything except the
// share list itself.
func TestSharedAccountsGetTheSameAccess(t *testing.T) {
	srv, guard := openSignIn(t, endpoint.NewRegistry(10))
	mine := signIn(t, srv, guard, "me@edspc.dev")
	theirs := signIn(t, srv, guard, "colleague@edspc.dev")

	view := mine.create(t, `{"name":"shared","spec":{"rules":[{"name":"ack","response":{"status":202}}]}}`)
	if got := theirs.list(t); len(got) != 0 {
		t.Fatalf("before sharing the colleague sees %d endpoints, want none", len(got))
	}

	w := mine.share(t, view.ID, `{"shared":["Colleague@edspc.dev"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("share status = %d, body = %s, want 200", w.Code, w.Body)
	}
	var updated endpointView
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Shared) != 1 || updated.Shared[0] != "colleague@edspc.dev" {
		t.Errorf("shared = %v, want the address lowercased", updated.Shared)
	}

	// It is now listed to them, and everything but sharing works.
	if got := theirs.list(t); len(got) != 1 || got[0].ID != view.ID {
		t.Fatalf("after sharing the colleague sees %d endpoints, want the shared one", len(got))
	}
	for _, tc := range []struct{ name, method, target, body string }{
		{"read", http.MethodGet, "/api/endpoints/" + view.ID, ""},
		{"requests", http.MethodGet, "/api/endpoints/" + view.ID + "/requests", ""},
		{"rename", http.MethodPut, "/api/endpoints/" + view.ID + "/name", `{"name":"renamed by a colleague"}`},
		{"rewrite the spec", http.MethodPut, "/api/endpoints/" + view.ID + "/spec", `{"response":{"status":204}}`},
		{"clear the history", http.MethodPost, "/api/endpoints/" + view.ID + "/reset", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := theirs.do(t, tc.method, tc.target, tc.body); w.Code != http.StatusOK {
				t.Errorf("status = %d, body = %s, want 200", w.Code, w.Body)
			}
		})
	}

	// The owner can still delete it themselves, once the rest is checked.
	t.Cleanup(func() {
		if w := mine.do(t, http.MethodDelete, "/api/endpoints/"+view.ID, ""); w.Code != http.StatusOK {
			t.Errorf("the owner deleting their own endpoint = %d, want 200", w.Code)
		}
	})

	// But deleting stays with the owner.
	t.Run("cannot delete", func(t *testing.T) {
		w := theirs.do(t, http.MethodDelete, "/api/endpoints/"+view.ID, "")
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s, want 403", w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "only the owner") {
			t.Errorf("body = %s, want it to name the rule", w.Body)
		}
		if got := mine.list(t); len(got) != 1 {
			t.Errorf("the owner has %d endpoints, want theirs still there", len(got))
		}
	})

	// And they cannot widen their own reach.
	t.Run("cannot re-share", func(t *testing.T) {
		w := theirs.share(t, view.ID, `{"shared":["colleague@edspc.dev","stranger@else.com"]}`)
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, body = %s, want 403", w.Code, w.Body)
		}
		if !strings.Contains(w.Body.String(), "only the owner") {
			t.Errorf("body = %s, want it to name the rule", w.Body)
		}
		stranger := signIn(t, srv, guard, "stranger@else.com")
		if got := stranger.list(t); len(got) != 0 {
			t.Errorf("the stranger sees %d endpoints, want none", len(got))
		}
	})
}

// Revoking is the same call with a shorter list.
func TestUnsharing(t *testing.T) {
	srv, guard := openSignIn(t, endpoint.NewRegistry(10))
	mine := signIn(t, srv, guard, "me@edspc.dev")
	theirs := signIn(t, srv, guard, "colleague@edspc.dev")

	view := mine.create(t, "")
	if w := mine.share(t, view.ID, `{"shared":["colleague@edspc.dev"]}`); w.Code != http.StatusOK {
		t.Fatalf("share status = %d", w.Code)
	}
	if got := theirs.list(t); len(got) != 1 {
		t.Fatalf("colleague sees %d endpoints, want the shared one", len(got))
	}

	if w := mine.share(t, view.ID, `{"shared":[]}`); w.Code != http.StatusOK {
		t.Fatalf("unshare status = %d, body = %s", w.Code, w.Body)
	}
	if got := theirs.list(t); len(got) != 0 {
		t.Errorf("after revoking the colleague still sees %d endpoints", len(got))
	}
	if w := theirs.do(t, http.MethodGet, "/api/endpoints/"+view.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 once access is revoked", w.Code)
	}
	// The owner still has it.
	if got := mine.list(t); len(got) != 1 {
		t.Errorf("the owner sees %d endpoints, want their own", len(got))
	}
}

func TestShareRejectsBadInput(t *testing.T) {
	srv, guard := openSignIn(t, endpoint.NewRegistry(10))
	mine := signIn(t, srv, guard, "me@edspc.dev")
	view := mine.create(t, "")

	tooMany := make([]string, endpoint.MaxShared+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("person%d@example.com", i)
	}
	list, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct{ name, body, wantSub string }{
		{"not an address", `{"shared":["colleague"]}`, "not an email address"},
		{"misspelled key", `{"share":["a@b.com"]}`, "unknown field"},
		{"not an object", `["a@b.com"]`, "parse share list"},
		{"too many", `{"shared":` + string(list) + `}`, "the maximum is"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := mine.share(t, view.ID, tt.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s, want 400", w.Code, w.Body)
			}
			if !strings.Contains(w.Body.String(), tt.wantSub) {
				t.Errorf("body = %s, want it to mention %q", w.Body, tt.wantSub)
			}
		})
	}

	// The owner is already there; naming them is a no-op, not an error.
	if w := mine.share(t, view.ID, `{"shared":["me@edspc.dev","  ","Me@Edspc.dev"]}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s, want 200", w.Code, w.Body)
	}
	var got endpointView
	if err := json.Unmarshal(mine.do(t, http.MethodGet, "/api/endpoints/"+view.ID, "").Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Shared) != 0 {
		t.Errorf("shared = %v, want the owner and the blanks dropped", got.Shared)
	}
}

// With no accounts there is nobody to share with, and every caller is the same
// anonymous one — so the call is refused rather than quietly doing nothing.
func TestSharingNeedsSignIn(t *testing.T) {
	srv := newTestServer(t)
	view := createEndpoint(t, srv, "")

	w := do(t, srv, http.MethodPut, "/api/endpoints/"+view.ID+"/share", `{"shared":["a@b.com"]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s, want 400", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "needs sign-in") {
		t.Errorf("body = %s, want it to say why", w.Body)
	}
}

// A shared account watches the same endpoint, so the stream has to reach it.
func TestEventStreamReachesSharedAccounts(t *testing.T) {
	registry := endpoint.NewRegistry(10)
	srv, guard := openSignIn(t, registry)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	mine := signIn(t, srv, guard, "me@edspc.dev")
	theirs := signIn(t, srv, guard, "colleague@edspc.dev")

	view := mine.create(t, "")
	if w := mine.share(t, view.ID, `{"shared":["colleague@edspc.dev"]}`); w.Code != http.StatusOK {
		t.Fatalf("share status = %d", w.Code)
	}

	es := openStreamAs(t, ts.URL, theirs.cookie)
	if w := do(t, srv, http.MethodPost, view.URL, ""); w.Code != http.StatusOK {
		t.Fatalf("callback status = %d", w.Code)
	}
	if ev := es.next(); ev.Type != "request" || ev.Endpoint != view.ID {
		t.Errorf("event = %+v, want the shared endpoint's callback", ev)
	}
}
