# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

README.md documents what the service does, its API, the spec format and the
template data available in responses. Read it first. This file covers only what
it does not: the architecture, the invariants behind it, and the conventions to
follow when changing it.

## Commands

README.md lists the everyday targets. Beyond those:

```sh
go test ./... -race -count=1                                      # before any concurrency change
go test ./internal/uuid -run TestV8OrdersWithinOneMillisecond -v  # a single test
go test ./internal/server -run 'TestUpdateSpec/valid'             # a single subtest
```

`make lint` fails on unformatted files rather than fixing them; run `make fmt`
first.

The one third-party dependency is `modernc.org/sqlite` (pure Go, no cgo), added
because the standard library has no SQLite and persistence was asked for. It
drags in nine indirect modules and raised the go directive to 1.25. Everything
else is stdlib, and adding a second dependency is a deliberate decision rather
than a default.

## Releases

`.github/workflows/release.yml` fires on a `v*` tag. It runs lint and tests
before building, so a tag cannot publish something CI would have rejected, and
cross-compiles every target from one runner with `CGO_ENABLED=0` — possible
only because the SQLite driver is pure Go. Keep it that way: a cgo dependency
would turn this into a matrix of per-OS runners.

The version is stamped with `-X main.version`, and `main.version` defaults to
`"dev"` so an unstamped binary never claims to be a release. Publishing uses
the `gh` CLI already on the runner rather than a third-party action.

## Dispatch

Three path spaces, resolved in `Server.ServeHTTP` in this order:

| Prefix | Handler |
| --- | --- |
| `/api/` | `admin.go`, `callback.go` |
| `/cb/{uuid}` | `serveCallback` (`callback.go`) |
| everything else | `serveUI` (`ui.go`) — the embedded console |

**There is no mocking layer**: no fixture files, no seeded data, no mock path
space. Everything the console shows is traffic that actually arrived. An
earlier version served file-defined stubs on every unclaimed path; that was
removed deliberately — do not reintroduce it.

`AdminPrefix` is matched via `isAdminPath`, which also reserves the bare `/api`
— otherwise the one path inside the reservation that is not below its slash
would behave differently from every other. Callback rules are unaffected by the
reservation: they match the path *below* `/cb/{id}`, so `/api/webhook` is an
ordinary rule path (`TestCallbackRulesCannotShadowConsole`).

Every control route is built by concatenating `AdminPrefix`, never by spelling
the prefix out, and the console is told the prefix by the server (see below),
so moving it stays a one-line change; keep it that way.

## Sign-in

`internal/auth` guards **only** the control API. Callback traffic is dispatched
in `ServeHTTP` before the guard and never passes through it: a third party
posting to `/cb/{id}` cannot sign in, and requiring it would defeat the
service. `TestCallbacksStayOpenWhenSignInIsRequired` pins that.

- A nil `*Auth` is a valid, disabled guard, so the no-credentials default needs
  no branching at the call sites.
- `New` refuses to enable sign-in without an allowed email list or domain.
  Google sign-in with no allowlist authenticates everyone on earth, which is
  not authentication; failing to start is the safe answer.
- `/api/auth/*` is the only control path Guard lets through unauthenticated —
  the console has to be able to ask whether sign-in is needed and to start it.
- The OAuth `state` is signed into a short-lived cookie and compared on the way
  back, so a forged callback cannot mint a session
  (`TestCallbackRejectsForgedState`).
- The session key is per-process, so sessions end with the process even when
  endpoints outlive it in SQLite. A signing key on disk is a liability that
  buys only staying signed in across a restart.
- The email comes from Google's userinfo endpoint rather than by parsing the
  id_token: it arrives straight from Google over TLS, so there is no JWT
  signature to verify and no key set to fetch.

## Architecture

`cmd/mockapi` (flags, signals, storage wiring) → `internal/server` (HTTP) →
`internal/endpoint` (the registry and the decision logic) →
`internal/mock` (matching, recording, templating).

`internal/store` (SQLite) and `internal/persist` (adapters) sit beside that.

**`internal/endpoint`** is the feature the service exists for.

- `Registry` maps `uuid.UUID` → `*Endpoint`. Endpoints are always **served**
  from memory; `Persist` adds write-through to a `Store` and `Restore` reads
  them back at startup. Without one the registry is memory-only, which is the
  default.
- Persistence is wired through interfaces `endpoint` declares itself (`Store`,
  `History`) so it never imports `store`; `internal/persist` holds the adapters
  that join the two. Keep that direction — the reverse creates an import cycle.
- `History` has no error returns because a callback must be answered whatever
  the database does. `persist.History` logs failures instead: a dropped write
  loses one recorded request, never a callback.
- `onSpec`/`onRecv` are separate hooks so a callback updates only the received
  counter rather than rewriting the spec on every request.
- `SetSpec` validates and compiles the whole spec *before* taking the lock, so
  a rejected edit cannot leave an endpoint half-updated.
- `Endpoint.Handle` decides (validate → match rules → default response) and
  records, but **writes nothing**. All HTTP writing lives in
  `server/callback.go`. Keep that split: it is what makes the decision logic
  testable without an HTTP round trip.
- `ResetRequests` clears history but deliberately leaves `Received()` alone —
  it reports lifetime traffic, not history size.

**`internal/config`** owns the shapes a spec is written in (`Rule`, `Request`,
`Response`, `Duration`) plus their validation and normalization — uppercased
methods, canonicalized matcher header names, status defaulted to 200 — so
nothing downstream re-derives them. There is deliberately no way to name a file
on the server: a response body is data the user supplies, never a path the
server reads (`TestSpecCannotReadServerFiles`).

**`internal/mock`** is what an endpoint uses to answer:

- `Pattern` (pattern.go) is a deliberately simpler path matcher than
  `http.ServeMux`'s. Because rules match in declaration order, a pattern only
  answers "do I match, and what did I capture" — never "am I more specific than
  that other one". Do not swap in `ServeMux` routing; the ordering is the
  feature.
- `Rule.MatchPath` matches against the path *below* the endpoint URL, which is
  why rules describe callback sub-paths rather than whole request paths.
- `Recorder` is a bounded ring; each endpoint owns one, so no endpoint's
  traffic can appear under another (`TestEachEndpointHasItsOwnHistory`).
- `Render` expands response-body templates.

### `internal/store`

Two SQLite databases, never one: endpoint settings and captured traffic have
different lifetimes and sizes, and either can be left unconfigured. Both open
with WAL and a busy timeout so a callback waits rather than failing. `Restore`
skips a stored row it cannot read or validate, with a warning, so one bad row
cannot make the service unbootable.

### `internal/uuid`

Endpoint IDs are UUIDv8 (RFC 9562 "custom format", so the layout is ours):
48-bit ms timestamp, then a **12-bit per-millisecond sequence counter**, then
random bits. The counter is not decoration — a millisecond is long enough to
mint many endpoints, and random bits there would make same-millisecond IDs sort
arbitrarily, which silently breaks `Registry.List`'s newest-first ordering.
`generator` is a type (not package globals) so tests can drive it with fixed
timestamps without touching the process-wide sequence.

### The console (`internal/server/web`)

Plain HTML/CSS/JS, `//go:embed`ed into the binary (`ui.go`) so it works from
any working directory. No build step, no framework, no npm.

- Served from the site root, below `/api/` and `/cb/` in the dispatch order.
  Because it does not live under `AdminPrefix`, it cannot derive the control-API
  base from its own URL: the server substitutes `__API_BASE__` in `index.html`
  at startup and the JS reads it from a `<meta name="api-base">` tag. Do not
  hardcode the prefix in the JS — `TestConsoleLearnsApiBaseFromServer` guards
  that. The substitution is a plain string replace, not a Go template, because
  the page's own help text contains literal `{{.Body.event}}` examples that a
  template engine would try to execute.
- It has no private routes: a feature that works in the console must work over
  curl.
- The whole spec is edited through forms; there is no JSON editor left. The
  forms therefore cover **every** field the server knows — validation, each
  rule's matcher and response, and the default response. A control that could
  not represent part of a saved spec would silently drop it on the next save,
  so adding a field to `config.Rule`, `config.Response` or
  `endpoint.Validation` means adding a control here. `importSpec` enforces the
  same contract from the other side: it rejects any key the forms cannot
  render rather than losing it. Read paths emit keys in the Go field order so
  an exported file is byte-identical to what the API returns.
- Everything the console toggles uses the `hidden` attribute, and a global
  `[hidden] { display: none !important }` makes it stick — component rules like
  `.signin { display: flex }` otherwise outrank the browser default and paint
  the element anyway. The sign-in overlay also hides `<main>` rather than just
  covering it, so the console behind it leaves the tab order.
- **Captured request data is attacker-controlled** (anyone who can reach a
  callback URL writes it). Everything rendered from the API goes into the DOM
  via `textContent`/`el()`, never `innerHTML`. A browser test asserts that a
  payload containing markup renders as text and does not execute — keep it that
  way when adding fields to the request view.
- `config.Request`/`config.Response` fields carry `omitempty` because the
  console reads the spec back into its editor after each save; without it the
  user's document accumulates `"delay":"0s"`-style noise they never wrote
  (`TestSpecRoundTripsWithoutZeroValueNoise`).
- Polling is 3s, skipped when the tab is hidden, never overwrites the spec
  editor while it holds unsaved changes, and re-checks the selection after every
  await so a slow response cannot paint one endpoint's traffic under another's
  header.

## Constraints worth knowing before editing

- **Inline JSON bodies cannot contain a double quote.** `json.RawMessage`
  preserves the `\"` escape, and the backslash reaches the template parser as a
  syntax error. That is why `Render` exposes the dash-free aliases README
  documents instead of `{{index .Header "X-Api-Key"}}`. Any new non-identifier
  key needs the same treatment.
- Templates use `missingkey=zero`, and `.Body` is nil when the request body is
  not JSON: an unknown key renders empty rather than failing the request,
  because an endpoint that 500s on a typo is harder to debug than one returning
  a visibly empty field. A malformed *template* still 500s.

## Testing conventions

Table-driven subtests with `t.Run`; failure messages read `got = X, want Y`.
Handler tests go through `httptest.NewRequest`/`ResponseRecorder` against a
`Server` from `newTestServer` (an empty registry, exactly how the command
starts), and drive the real control API via `createEndpoint` rather than
reaching into the registry, so they exercise the same path a user does. No real
listener and no fixtures on disk.

Concurrency is covered by tests, not just review: `Registry`, `Recorder`, and
the UUID generator each have a concurrent test. Run `-race` when touching them.
