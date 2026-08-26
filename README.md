# mockapi

[![CI](https://github.com/edspc/golang-mock-api-server/actions/workflows/ci.yml/badge.svg)](https://github.com/edspc/golang-mock-api-server/actions/workflows/ci.yml)

A service for **callback endpoints**: ask it for a URL, hand that URL to a
third party, watch what they actually send, then write the response and
validation logic for the requests that follow.

Endpoints are created at runtime — no config file, no restart, no redeploy.
Everything it shows you is real captured traffic; there is no mock data and no
mock path space.

## Quick start

```sh
make build && ./bin/mockapi
open http://localhost:8080/
```

Or over the API:

```sh
# 1. Get a URL. The id is a UUIDv8.
curl -X POST localhost:8080/api/endpoints -d '{"name":"stripe"}'
#=> {"id":"01a0333e-...","url":"/cb/01a0333e-...", ...}

# 2. Point the third party at it. Everything it sends is captured.
curl -X POST localhost:8080/cb/$ID -H 'X-Signature: sig' -d '{"event":"paid"}'

# 3. See exactly what arrived.
curl localhost:8080/api/endpoints/$ID/requests

# 4. Now write how it should answer, and what it should reject.
curl -X PUT localhost:8080/api/endpoints/$ID/spec -d '{
  "validation": {"requireHeaders": ["X-Signature"], "requireFields": ["event"]},
  "rules": [
    {"name": "paid",
     "request": {"bodyContains": "\"event\":\"paid\""},
     "response": {"status": 202, "body": {"ack": "{{.Body.event}}"}}}
  ],
  "response": {"status": 204}
}'
```

From then on: a callback missing `X-Signature` gets 400 with the reason, a
`paid` event gets 202 with its own payload echoed back, anything else gets 204
— and all of it stays visible in `/api/endpoints/$ID/requests`.

### Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `-addr` | `:8080` | listen address |
| `-history` | `200` | captured requests kept per endpoint |
| `-quiet` | `false` | log warnings and errors only |
| `-version` | | print the version and exit |

### Storage

By default everything is in memory and disappears with the process. Point the
environment at SQLite files to keep it:

| Variable | Holds |
| --- | --- |
| `MOCKAPI_ENDPOINTS_DB` | endpoints and the spec each one serves |
| `MOCKAPI_REQUESTS_DB` | the captured requests |

```sh
MOCKAPI_ENDPOINTS_DB=./endpoints.db MOCKAPI_REQUESTS_DB=./requests.db ./bin/mockapi
```

They are separate databases on purpose: endpoint settings are small and worth
backing up, captured traffic is bulky and disposable. Each is independent —
set one, the other, both or neither. Deleting an endpoint also drops its
captured requests. Files are created on first use.

### Sign-in

Without Google credentials the control API is open, which is the default. Set
them and managing endpoints requires signing in — **callback URLs stay open
either way**, since a third party posting to `/cb/{id}` has no way to sign in.

| Variable | Meaning |
| --- | --- |
| `GOOGLE_CLIENT_ID` | OAuth client id; sign-in is off unless set |
| `GOOGLE_CLIENT_SECRET` | OAuth client secret |
| `MOCKAPI_BASE_URL` | externally visible origin, e.g. `https://mock.example.com` — include the path prefix if the app is mounted under one (defaults to the listen address) |
| `MOCKAPI_ALLOWED_EMAILS` | comma-separated addresses that may manage endpoints |
| `MOCKAPI_ALLOWED_DOMAIN` | a whole domain that may, e.g. `example.com` |

Register `{MOCKAPI_BASE_URL}/api/auth/callback` as the redirect URI in the
Google console; the startup log prints the exact value.

At least one of `MOCKAPI_ALLOWED_EMAILS` or `MOCKAPI_ALLOWED_DOMAIN` is
required — the server refuses to start otherwise, because sign-in with no
allowlist would let any Google account on earth manage your endpoints. The
allowlist is re-checked on every request, so removing someone takes effect
immediately. Sessions are signed cookies valid for 12 hours and end with the
process.

## The console

At `/`: create endpoints, rename them, watch requests arrive, filter them by
method, status or validation failure, inspect each one's headers, query and
body, and build the validation, rules and responses through forms — no JSON
typing, with import and export for moving a spec between endpoints.
It is compiled into the binary — no build step, no npm, nothing to serve
separately — and is a pure client of the API below, so anything it does you can
also do with curl.

Requests appear as they arrive: the console subscribes to the event stream and
shows a **live** badge while it is connected. If the stream cannot be held open
it falls back to polling on its own, and nothing else changes.

### Behind a path prefix

The console asks for everything relative to the page it was served from, so
the whole app can be mounted under a sub-path with no configuration on this
side — there is no base-path flag to set:

```nginx
location /mockapi/ {
    proxy_pass http://127.0.0.1:8080/;   # the trailing slash strips the prefix
    proxy_http_version 1.1;
    proxy_set_header Host $host;
}
location = /mockapi { return 301 /mockapi/; }
```

The trailing slash on `proxy_pass` is what matters: the server always serves
itself from `/`, and never has to be told where it was mounted. Callback URLs
the console shows include the prefix, since that is the URL a third party has
to call. The event stream sets `X-Accel-Buffering: no`, so nginx streams it
through rather than buffering it forever.

With sign-in on, `MOCKAPI_BASE_URL` carries the prefix too
(`https://example.com/mockapi`) — Google needs an absolute redirect URI, the
only URL here that cannot be relative.

## Endpoints

Each endpoint is identified by a **UUIDv8** and served at `/cb/{id}`. Any
sub-path below it belongs to the same endpoint, so a provider that posts to
`/cb/{id}/success` and `/cb/{id}/failure` lands on one endpoint with two rules.

Endpoints live in memory unless a database is configured (see
[Storage](#storage)).

| Endpoint | Purpose |
| --- | --- |
| `POST /api/endpoints` | create one; optional `{"name":…, "spec":…}` |
| `GET /api/endpoints` | list, newest first |
| `GET /api/endpoints/{id}` | current spec and lifetime request count |
| `DELETE /api/endpoints/{id}` | drop it and everything it captured |
| `PUT /api/endpoints/{id}/name` | relabel it: `{"name":"stripe"}`; the URL never changes |
| `PUT /api/endpoints/{id}/spec` | replace the response and validation logic |
| `GET /api/endpoints/{id}/requests` | captured requests, filtered by `method`, `status` and `invalid` |
| `POST /api/endpoints/{id}/reset` | clear the captured history |
| `GET /api/events` | server-sent events: every change, as it happens |
| `GET /api/health` | liveness plus the endpoint count |

A name is a label for your own use — renaming touches nothing a caller can
see, so an endpoint someone else is already posting to can be relabelled at
any time.

### Filtering captured requests

| Parameter | Matches |
| --- | --- |
| `method=post` | that method, any case |
| `status=404` | that exact status |
| `status=4xx` | that whole class (`1xx` … `5xx`) |
| `invalid=true` | only requests that failed validation |

```sh
curl "localhost:8080/api/endpoints/$ID/requests?method=POST&status=4xx"
```

They combine, and the console's filters are these same parameters — it has no
filtering of its own to disagree with. An unknown parameter is a 400 rather
than a silently unfiltered listing, so a typo cannot look like "no traffic".

### Live events

`GET /api/events` is a [Server-Sent Events](https://developer.mozilla.org/docs/Web/API/Server-sent_events)
stream. It is how the console learns about a callback the moment it lands
instead of on a timer, and it works just as well from a script:

```sh
curl -N localhost:8080/api/events
#=> data: {"type":"request","endpoint":"01a0333e-…","received":7}
```

| `type` | Meaning |
| --- | --- |
| `request` | a callback arrived |
| `created` / `deleted` | an endpoint appeared or went away |
| `updated` | renamed, or its spec replaced |
| `reset` | its captured history was cleared |

Events say *what* changed, not what it changed to: read the endpoint back for
the detail. Delivery is best-effort — a client that stops reading loses events
rather than slowing down the callbacks that produce them — so treat the stream
as a signal to re-read, not as a log. An idle stream sends a comment line every
25 seconds to keep proxies from closing it.

`/api/` is reserved for the control API and `/cb/` for endpoint traffic; both
are matched before the console, so neither can be shadowed by it. Any other
path that is not a console asset is a 404.

## Spec

```json
{
  "validation": {
    "requireHeaders": ["X-Signature", "X-Env: prod"],
    "requireQuery": ["source"],
    "requireFields": ["event", "data.id"],
    "jsonBody": true,
    "bodyContains": "charge",
    "onFailure": { "status": 422, "body": { "error": "bad payload" } }
  },
  "rules": [
    {
      "name": "paid",
      "request": {
        "method": "POST",
        "path": "/success",
        "query": { "source": "*" },
        "headers": { "X-Api-Key": "secret" },
        "bodyContains": "paid"
      },
      "response": {
        "status": 202,
        "headers": { "X-Handled-By": "mockapi" },
        "body": { "ack": "{{.Body.event}}" },
        "delay": "250ms"
      }
    }
  ],
  "response": { "status": 204 }
}
```

- `validation` runs first. A failing request is **still captured**, with the
  reasons attached, and gets `onFailure` (or 400 listing every reason — all of
  them at once, not one per attempt).
- `rules` are tried in order, first match wins. Every declared field under
  `request` must match; omitted fields are not constrained. A rule with no
  `path` matches any sub-path.
- `path` supports `{name}` captures and a trailing `*`. `query` and `headers`
  values may be `"*"`, meaning "present with any value".
- `delay` accepts `"250ms"` or a plain number of milliseconds, and is
  interrupted if the caller hangs up.
- `response` answers anything that validated but matched no rule. Omit it and
  the endpoint replies `200 {"received":true}`.
- Unknown keys are rejected, so a typo fails the save instead of silently
  producing a rule that never matches.
- `PUT` replaces the spec wholesale, so what you send is exactly what the
  endpoint does. An invalid spec is rejected and the previous one keeps
  serving — a bad edit never breaks a URL a third party is already calling.

### Response templates

Response bodies are Go `text/template`. Available data:

| Expression | Value |
| --- | --- |
| `{{.Body.event}}` | field of the JSON request body (`{{.Body.data.id}}` nests) |
| `{{.Path.id}}` | a `{id}` capture from the rule's path |
| `{{.Path.rest}}` | the trailing `*` capture |
| `{{.Query.source}}` | first value of a query parameter |
| `{{.Header.XApiKey}}` | first value of the `X-Api-Key` header |
| `{{now}}` | current UTC time, RFC3339 |

Inline JSON bodies cannot contain a double quote, so header and wildcard keys
are also exposed under dash-free aliases (`X-Api-Key` → `XApiKey`, `*` →
`rest`) — use those rather than `{{index .Header "X-Api-Key"}}`.

## Development

```sh
make test          # go test ./...
make lint          # go vet + gofmt check
make cover         # coverage summary
```

## Releases

Pushing a `v*` tag builds the binaries and publishes a GitHub release:

```sh
git tag v0.1.0 && git push origin v0.1.0
```

Lint and the test suite run first, so a tag cannot publish a build that does
not pass CI. Binaries are published for `linux/amd64`, `linux/arm64` and
`darwin/arm64`, with a `SHA256SUMS` file. There is no cgo, so every target is a
plain cross-compile and the binaries have no runtime dependencies:

```sh
curl -fsSLO https://github.com/edspc/golang-mock-api-server/releases/latest/download/mockapi_v0.1.0_linux_amd64
chmod +x mockapi_v0.1.0_linux_amd64
```

`mockapi -version` reports the tag it was built from.

## License

MIT — see [LICENSE](LICENSE).
