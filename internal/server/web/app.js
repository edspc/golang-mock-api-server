// mockapi console.
//
// Captured requests are written by whoever calls a callback URL, so every
// value taken from the API is put into the DOM with textContent — never
// innerHTML. A webhook body containing markup must render as text, not run.
'use strict';

// The control API's base is rendered into the page by the server. The console
// lives at the site root, so it cannot derive the prefix from its own URL, and
// a literal here would silently break if the Go-side prefix moved.
const API = document.querySelector('meta[name="api-base"]').content;
const POLL_MS = 3000;
// How many poll ticks to skip while the event stream is delivering updates.
const SAFETY_TICKS = 5;
// How long a burst of events is folded into one refresh.
const COALESCE_MS = 250;

// The whole app can be mounted under a sub-path — an nginx `location /mockapi/`
// proxying to the server's root — and the server has no way to know it was.
// Everything it hands back (the API prefix above, a callback URL, a sign-in
// link) is written from *its* root, so resolve those against the page's own
// base instead of the origin. At the site root this is the identity.
const ROOT = new URL('.', document.baseURI);
function href(serverPath) {
  return new URL(String(serverPath).replace(/^\/+/, ''), ROOT).href;
}

const state = {
  endpoints: [],
  selectedId: null,
  filterMethod: '',
  filterStatus: '',
  filterInvalid: false,
  stream: null,
  live: false,
  auth: true,
  expanded: new Set(),
  lastRequestsJSON: '',
  specDirty: false,
  renaming: false,
};

/* ---------- small helpers ---------- */

function el(tag, props, children) {
  const node = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (k === 'class') node.className = v;
      else if (k === 'text') node.textContent = v;
      else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
      else if (v !== null && v !== undefined) node.setAttribute(k, v);
    }
  }
  for (const c of children || []) {
    if (c) node.appendChild(c);
  }
  return node;
}

function $(id) { return document.getElementById(id); }

function clear(node) {
  while (node.firstChild) node.removeChild(node.firstChild);
}

let toastTimer = null;
function toast(message) {
  const t = $('toast');
  t.textContent = message;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 2200);
}

// api sends and receives JSON, surfacing the server's {"error": …} message.
async function api(method, path, rawBody) {
  const init = { method };
  if (rawBody !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = rawBody;
  }
  const res = await fetch(href(path), init);
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch (e) { data = null; }
  }
  if (res.status === 401) {
    // The session expired or was never there: stop the polling loop and show
    // the sign-in screen rather than a wall of failed requests.
    requireSignIn(data && data.login);
    const err = new Error((data && data.error) || 'sign in to manage endpoints');
    err.unauthorized = true;
    throw err;
  }
  if (!res.ok) {
    throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  }
  return data;
}

/* ---------- sign-in ---------- */

// requireSignIn shows the overlay. Callback URLs are unaffected by any of
// this — only the control API is guarded.
function requireSignIn(loginPath) {
  state.auth = false;
  // EventSource would otherwise reconnect to a route that now answers 401,
  // over and over, for as long as the overlay is up.
  stopEventStream();
  $('signin-link').href = href(loginPath || `${API}/auth/login`);
  $('signin').hidden = false;
  $('account').hidden = true;
  // Hidden, not merely covered: an opaque overlay still leaves the console
  // behind it in the tab order.
  document.querySelector('main').hidden = true;
}

async function loadAuth() {
  let status;
  try {
    const res = await fetch(href(`${API}/auth/status`));
    status = await res.json();
  } catch (e) {
    return false; // the server is unreachable; loadHealth reports it
  }
  if (status.enabled && !status.authenticated) {
    requireSignIn();
    return false;
  }
  state.auth = true;
  $('signin').hidden = true;
  document.querySelector('main').hidden = false;
  if (status.enabled) {
    $('account-email').textContent = status.email || '';
    $('account').hidden = false;
  }
  return true;
}

function formatTime(iso) {
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  return d.toLocaleTimeString([], { hour12: false }) +
    '.' + String(d.getMilliseconds()).padStart(3, '0');
}

function statusClass(status) {
  if (status >= 500 || status === 0) return 'err';
  if (status >= 400) return 'warn';
  if (status >= 200) return 'ok';
  return '';
}

// prettyJSON reformats a JSON payload for display, leaving non-JSON as-is.
function prettyJSON(text) {
  try { return JSON.stringify(JSON.parse(text), null, 2); } catch (e) { return text; }
}

/* ---------- health ---------- */

async function loadHealth() {
  try {
    const h = await api('GET', `${API}/health`);
    $('health').textContent = `${h.endpoints} endpoint${h.endpoints === 1 ? '' : 's'}`;
  } catch (e) {
    $('health').textContent = 'server unreachable';
  }
}

/* ---------- endpoint list ---------- */

async function loadEndpoints() {
  state.endpoints = await api('GET', `${API}/endpoints`) || [];
  renderEndpoints();
  if (state.selectedId && !state.endpoints.some((e) => e.id === state.selectedId)) {
    selectEndpoint(null);
  }
}

function renderEndpoints() {
  const list = $('endpoint-list');
  clear(list);

  if (state.endpoints.length === 0) {
    list.appendChild(el('div', { class: 'muted', text: 'No endpoints yet.' }));
    return;
  }

  for (const ep of state.endpoints) {
    const name = ep.name
      ? el('div', { class: 'ep-name', text: ep.name })
      : el('div', { class: 'ep-name untitled', text: 'untitled' });

    list.appendChild(el('button', {
      class: 'ep' + (ep.id === state.selectedId ? ' is-active' : ''),
      onclick: () => selectEndpointInteractive(ep.id),
    }, [
      name,
      el('div', { class: 'ep-id', text: ep.id }),
      el('div', { class: 'ep-count', text: `${ep.received} received` }),
    ]));
  }
}

function selectedEndpoint() {
  return state.endpoints.find((e) => e.id === state.selectedId) || null;
}

// selectEndpointInteractive is the user-initiated path, which must not throw
// away spec edits the user has not saved. selectEndpoint itself stays silent:
// it is also called with null when the selected endpoint disappears mid-poll,
// where there is nothing left to save into.
function selectEndpointInteractive(id) {
  if (id === state.selectedId) return;
  if (state.specDirty && !confirm('Discard unsaved changes to this endpoint’s spec?')) {
    return;
  }
  selectEndpoint(id);
}

async function selectEndpoint(id) {
  state.selectedId = id;
  state.expanded.clear();
  state.lastRequestsJSON = '';
  stopRename();
  renderEndpoints();

  const detail = $('endpoint-detail');
  const empty = $('empty-state');
  if (!id) {
    detail.hidden = true;
    empty.hidden = false;
    return;
  }
  empty.hidden = true;
  detail.hidden = false;

  renderEndpointHead();
  loadSpecIntoEditor();
  await loadRequests();
}

function renderEndpointHead() {
  const ep = selectedEndpoint();
  if (!ep) return;
  $('ep-name').textContent = ep.name || 'untitled endpoint';
  // Rebased, not pinned to the origin: this is the URL the user copies and
  // hands to a third party, so it has to include the mount point.
  $('ep-url').textContent = href(ep.url);
  $('ep-id').textContent = ep.id;
  $('ep-created').textContent = formatTime(ep.createdAt);
  $('ep-received').textContent = ep.received;
}

/* ---------- rename ---------- */

// Renaming is an explicit mode rather than an always-live input: the head is
// re-rendered on every poll, which would fight with a half-typed name.
function startRename() {
  const ep = selectedEndpoint();
  if (!ep || state.renaming) return;
  state.renaming = true;
  const input = $('rename-input');
  input.value = ep.name || '';
  $('name-display').hidden = true;
  $('rename-form').hidden = false;
  input.focus();
  input.select();
}

function stopRename() {
  state.renaming = false;
  $('rename-form').hidden = true;
  $('name-display').hidden = false;
}

async function saveName() {
  const ep = selectedEndpoint();
  if (!ep) return;
  const name = $('rename-input').value;
  try {
    const updated = await api('PUT', `${API}/endpoints/${ep.id}/name`, JSON.stringify({ name }));
    // The listing is polled, but update it now so the sidebar and the heading
    // do not disagree for the next three seconds.
    ep.name = updated.name || '';
    stopRename();
    renderEndpoints();
    renderEndpointHead();
    toast('Endpoint renamed');
  } catch (e) {
    toast(e.message);
  }
}

/* ---------- requests ---------- */

// filterQuery is the current filter as the listing's query string. The server
// does the filtering, so the console can never disagree with what curl shows.
function filterQuery() {
  const q = new URLSearchParams();
  if (state.filterMethod) q.set('method', state.filterMethod);
  if (state.filterStatus) q.set('status', state.filterStatus);
  if (state.filterInvalid) q.set('invalid', 'true');
  const s = q.toString();
  return s ? '?' + s : '';
}

async function loadRequests() {
  const ep = selectedEndpoint();
  if (!ep) return;
  const id = ep.id;
  const query = filterQuery();
  let entries;
  try {
    entries = await api('GET', `${API}/endpoints/${id}/requests${query}`) || [];
  } catch (e) {
    return; // endpoint deleted mid-poll; the list refresh will notice
  }

  // The selection (or filter) can change while the request is in flight;
  // rendering a stale response would show one endpoint's traffic under
  // another's header, or under filters that no longer apply.
  if (state.selectedId !== id || filterQuery() !== query) return;

  const json = JSON.stringify(entries);
  if (json === state.lastRequestsJSON) return; // avoid re-rendering on every poll
  state.lastRequestsJSON = json;

  renderRequests($('requests'), entries, {
    showValidation: true,
    emptyText: query ? 'No captured request matches these filters.' : 'Nothing captured yet.',
  });
}

// applyFilter re-reads the listing under the new filter. The cached copy is
// dropped first: the same entries under a different filter are a different
// answer.
function applyFilter() {
  state.lastRequestsJSON = '';
  loadRequests().catch((e) => { if (!e.unauthorized) toast(e.message); });
}

// renderRequests draws newest first. Every field here is third-party input.
//
// Expanding a row re-renders this same list into this same container, so the
// function keeps ownership of its own panel and a toggle needs no refetch.
function renderRequests(container, entries, opts) {
  const { showValidation, emptyText } = opts;
  clear(container);

  if (entries.length === 0) {
    container.appendChild(el('div', { class: 'muted', text: emptyText }));
    return;
  }

  for (const entry of entries.slice().reverse()) {
    const key = entry.time + '|' + entry.path;
    const isOpen = state.expanded.has(key);
    const invalid = showValidation && entry.validationErrors && entry.validationErrors.length > 0;

    const head = el('button', {
      class: 'req-head',
      onclick: () => {
        if (state.expanded.has(key)) state.expanded.delete(key);
        else state.expanded.add(key);
        renderRequests(container, entries, opts);
      },
    }, [
      el('span', { class: 'badge ' + statusClass(entry.status), text: String(entry.status) }),
      el('span', { class: 'method', text: entry.method }),
      el('span', { class: 'req-path', text: entry.path }),
      invalid
        ? el('span', { class: 'invalid-tag', text: `${entry.validationErrors.length} validation error${entry.validationErrors.length === 1 ? '' : 's'}` })
        : (entry.rule ? el('span', { class: 'rule-tag' }, [
            document.createTextNode('matched '),
            el('strong', { text: entry.rule }),
          ]) : el('span', { class: 'rule-tag', text: 'no rule matched' })),
      el('span', { class: 'req-time', text: formatTime(entry.time) }),
    ]);

    const card = el('div', { class: 'req' }, [head]);
    if (isOpen) card.appendChild(requestBody(entry, invalid));
    container.appendChild(card);
  }
}

function requestBody(entry, invalid) {
  const parts = [];

  if (invalid) {
    parts.push(el('section', {}, [
      el('h4', { text: 'Validation errors' }),
      el('ul', { class: 'errors' }, entry.validationErrors.map((m) => el('li', { text: m }))),
    ]));
  }

  const query = entry.query || {};
  if (Object.keys(query).length > 0) {
    parts.push(el('section', {}, [
      el('h4', { text: 'Query' }),
      kvList(query),
    ]));
  }

  const headers = entry.headers || {};
  if (Object.keys(headers).length > 0) {
    parts.push(el('section', {}, [
      el('h4', { text: 'Headers' }),
      kvList(headers),
    ]));
  }

  parts.push(el('section', {}, [
    el('h4', { text: 'Body' }),
    entry.body
      ? el('pre', { text: prettyJSON(entry.body) })
      : el('div', { class: 'muted', text: 'empty' }),
  ]));

  return el('div', { class: 'req-body' }, parts);
}

function kvList(map) {
  const rows = Object.keys(map).sort().map((k) =>
    el('div', {}, [
      el('span', { class: 'k', text: k + ': ' }),
      el('span', { text: (map[k] || []).join(', ') }),
    ]));
  return el('div', { class: 'kv' }, rows);
}

/* ---------- form widgets ---------- */

// The whole spec is edited through forms, so the forms must cover every field
// the server knows. A control that could not represent part of a saved spec
// would silently drop it on the next save; readSpec/renderSpec below are the
// two halves of that contract, and importSpec rejects anything they cannot
// round-trip rather than losing it quietly.

const METHODS = ['', 'GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];
const SPEC_KEYS = ['validation', 'rules', 'response'];
const VALIDATION_KEYS = ['requireHeaders', 'requireQuery', 'jsonBody', 'requireFields', 'bodyContains', 'onFailure'];
const RULE_KEYS = ['name', 'request', 'response'];
const REQUEST_KEYS = ['method', 'path', 'query', 'headers', 'bodyContains'];
const RESPONSE_KEYS = ['status', 'headers', 'body', 'delay'];

function dirty() { setSpecDirty(true); }

// markDirty wires every control inside scope to the unsaved-changes marker.
function markDirty(scope) {
  for (const node of scope.querySelectorAll('input, textarea, select')) {
    node.addEventListener('input', dirty);
    node.addEventListener('change', dirty);
  }
  return scope;
}

// makeRow appends one removable row of text inputs to a list.
function makeRow(list, fields) {
  const row = el('div', { class: 'row' });
  for (const f of fields) {
    const input = el('input', { type: 'text', placeholder: f.placeholder, autocomplete: 'off' });
    input.value = f.value || '';
    input.addEventListener('input', dirty);
    row.appendChild(input);
  }
  row.appendChild(el('button', {
    type: 'button', class: 'ghost row-del', title: 'Remove', text: '×',
    onclick: () => { row.remove(); dirty(); },
  }));
  list.appendChild(row);
  return row;
}

// rowValues returns the trimmed inputs of each row in a list.
function rowValues(list) {
  return Array.from(list.querySelectorAll('.row')).map((row) =>
    Array.from(row.querySelectorAll('input')).map((i) => i.value.trim()));
}

// kvField builds a labelled list of name/value rows plus its add button.
function kvField(label, name, values, placeholders) {
  const list = el('div', { class: 'rows', 'data-list': name });
  for (const [k, v] of Object.entries(values || {})) {
    makeRow(list, [{ placeholder: placeholders[0], value: k }, { placeholder: placeholders[1], value: v }]);
  }
  return el('div', { class: 'field' }, [
    el('label', { text: label }),
    list,
    el('button', {
      type: 'button', class: 'ghost add', text: '+ ' + placeholders[2],
      onclick: () => { makeRow(list, [{ placeholder: placeholders[0] }, { placeholder: placeholders[1] }]); dirty(); },
    }),
  ]);
}

function readKV(list) {
  const out = {};
  for (const [name, value] of rowValues(list)) {
    if (name) out[name] = value;
  }
  return Object.keys(out).length ? out : null;
}

/* ---------- response block (used for rules, the default, and on-failure) ---------- */

function responseBlock(resp) {
  const r = resp || {};

  const status = el('input', { type: 'number', min: '100', max: '599', 'data-f': 'status', placeholder: '200' });
  status.value = r.status || '';
  const delay = el('input', { type: 'text', 'data-f': 'delay', placeholder: 'e.g. 250ms', autocomplete: 'off' });
  delay.value = r.delay || '';
  const body = el('textarea', { class: 'small', spellcheck: 'false', 'data-f': 'body', placeholder: '{"ok": true}' });
  body.value = r.body === undefined ? '' : JSON.stringify(r.body, null, 2);

  return markDirty(el('div', { class: 'resp-block' }, [
    el('div', { class: 'field inline' }, [
      el('label', { text: 'Status' }), status,
      el('label', { text: 'Delay' }), delay,
    ]),
    kvField('Headers', 'headers', r.headers, ['Content-Type', 'application/json', 'header']),
    el('div', { class: 'field' }, [el('label', { text: 'Body (JSON)' }), body]),
  ]));
}

// readResponse returns null when nothing is filled in, so an untouched block
// leaves the field out of the spec instead of saving an empty object.
function readResponse(scope, label) {
  const r = {};

  const status = scope.querySelector('[data-f="status"]').value.trim();
  if (status) {
    const n = Number(status);
    if (!Number.isInteger(n) || n < 100 || n > 599) {
      throw new Error(`${label}: status must be between 100 and 599, got "${status}".`);
    }
    r.status = n;
  }

  // Keys are emitted in the server's field order, so an exported file is
  // byte-identical to what the API returns and diffs cleanly.
  const headers = readKV(scope.querySelector('[data-list="headers"]'));
  if (headers) r.headers = headers;

  const body = scope.querySelector('[data-f="body"]').value.trim();
  if (body) {
    try {
      r.body = JSON.parse(body);
    } catch (e) {
      throw new Error(`${label}: body is not valid JSON — ${e.message}`);
    }
  }

  const delay = scope.querySelector('[data-f="delay"]').value.trim();
  if (delay) r.delay = delay;

  return Object.keys(r).length ? r : null;
}

/* ---------- rules ---------- */

function renumberRules() {
  Array.from($('rules').children).forEach((card, i) => {
    card.querySelector('.rule-index').textContent = `Rule ${i + 1}`;
  });
}

function ruleCard(rule) {
  const d = rule || {};
  const req = d.request || {};

  const name = el('input', { type: 'text', 'data-f': 'name', placeholder: 'name (optional)', autocomplete: 'off' });
  name.value = d.name || '';

  const method = el('select', { 'data-f': 'method' });
  for (const m of METHODS) {
    const opt = el('option', { value: m, text: m || 'ANY method' });
    if ((req.method || '') === m) opt.selected = true;
    method.appendChild(opt);
  }

  const path = el('input', { type: 'text', 'data-f': 'path', placeholder: '/success, /users/{id}, /*', autocomplete: 'off' });
  path.value = req.path || '';
  const contains = el('input', { type: 'text', 'data-f': 'bodyContains', placeholder: 'substring of the raw body', autocomplete: 'off' });
  contains.value = req.bodyContains || '';

  const card = el('div', { class: 'rule' });
  const move = (delta) => {
    const sibling = delta < 0 ? card.previousElementSibling : card.nextElementSibling;
    if (!sibling) return;
    if (delta < 0) card.parentNode.insertBefore(card, sibling);
    else card.parentNode.insertBefore(sibling, card);
    renumberRules();
    dirty();
  };

  card.appendChild(el('div', { class: 'rule-head' }, [
    el('span', { class: 'rule-index' }),
    name,
    el('button', { type: 'button', class: 'ghost row-del', text: '↑', title: 'Move up', onclick: () => move(-1) }),
    el('button', { type: 'button', class: 'ghost row-del', text: '↓', title: 'Move down', onclick: () => move(1) }),
    el('button', {
      type: 'button', class: 'ghost row-del', text: '×', title: 'Remove rule',
      onclick: () => { card.remove(); renumberRules(); dirty(); },
    }),
  ]));

  card.appendChild(el('div', { class: 'rule-body' }, [
    el('h4', { text: 'Matches when' }),
    el('div', { class: 'field inline' }, [
      el('label', { text: 'Method' }), method,
      el('label', { text: 'Path' }), path,
    ]),
    kvField('Query parameters', 'req-query', req.query, ['source', '* or a value', 'parameter']),
    kvField('Headers', 'req-headers', req.headers, ['X-Api-Key', '* or a value', 'header']),
    el('div', { class: 'field' }, [el('label', { text: 'Body contains' }), contains]),
    el('h4', { text: 'Then responds' }),
    responseBlock(d.response),
  ]));

  markDirty(card.querySelector('.rule-head'));
  markDirty(card.querySelector('.rule-body > .field.inline'));
  contains.addEventListener('input', dirty);
  return card;
}

function readRule(card, index) {
  const label = `Rule ${index + 1}`;
  const rule = {};

  const name = card.querySelector('[data-f="name"]').value.trim();
  if (name) rule.name = name;

  const request = {};
  const method = card.querySelector('[data-f="method"]').value;
  if (method) request.method = method;
  const path = card.querySelector('[data-f="path"]').value.trim();
  if (path) request.path = path;
  const query = readKV(card.querySelector('[data-list="req-query"]'));
  if (query) request.query = query;
  const headers = readKV(card.querySelector('[data-list="req-headers"]'));
  if (headers) request.headers = headers;
  const contains = card.querySelector('[data-f="bodyContains"]').value.trim();
  if (contains) request.bodyContains = contains;
  if (Object.keys(request).length) rule.request = request;

  const response = readResponse(card.querySelector('.resp-block'), `${label} response`);
  if (response) rule.response = response;

  return rule;
}

/* ---------- validation ---------- */

function addHeaderRow(name, value) {
  makeRow($('v-headers'), [
    { placeholder: 'X-Signature', value: name },
    { placeholder: 'value (optional)', value: value },
  ]);
}

function addQueryRow(name) { makeRow($('v-query'), [{ placeholder: 'source', value: name }]); }
function addFieldRow(path) { makeRow($('v-fields'), [{ placeholder: 'data.id', value: path }]); }

function renderValidationForm(validation) {
  const v = validation || {};
  for (const id of ['v-headers', 'v-query', 'v-fields']) clear($(id));

  for (const entry of v.requireHeaders || []) {
    // The wire format is "Name" or "Name: value".
    const i = entry.indexOf(':');
    if (i >= 0) addHeaderRow(entry.slice(0, i).trim(), entry.slice(i + 1).trim());
    else addHeaderRow(entry.trim(), '');
  }
  for (const q of v.requireQuery || []) addQueryRow(q);
  for (const f of v.requireFields || []) addFieldRow(f);

  $('v-body-contains').value = v.bodyContains || '';
  $('v-json-body').checked = !!v.jsonBody;

  const fail = $('v-fail-block');
  clear(fail);
  fail.appendChild(responseBlock(v.onFailure));
  $('v-onfailure').open = !!v.onFailure;
}

function readValidationForm() {
  const v = {};

  const headers = rowValues($('v-headers'))
    .filter(([name]) => name)
    .map(([name, value]) => (value ? `${name}: ${value}` : name));
  if (headers.length) v.requireHeaders = headers;

  const query = rowValues($('v-query')).map(([name]) => name).filter(Boolean);
  if (query.length) v.requireQuery = query;

  if ($('v-json-body').checked) v.jsonBody = true;

  const fields = rowValues($('v-fields')).map(([path]) => path).filter(Boolean);
  if (fields.length) v.requireFields = fields;

  const contains = $('v-body-contains').value.trim();
  if (contains) v.bodyContains = contains;

  const onFailure = readResponse($('v-fail-block'), 'Failure response');
  if (onFailure) v.onFailure = onFailure;

  return Object.keys(v).length ? v : null;
}

/* ---------- the whole spec ---------- */

const EXAMPLE = {
  validation: {
    requireHeaders: ['X-Signature'],
    requireFields: ['event'],
    onFailure: { status: 400, body: { error: 'bad payload' } },
  },
  rules: [
    {
      name: 'paid',
      request: { method: 'POST', bodyContains: '"event":"paid"' },
      response: { status: 202, body: { ack: '{{.Body.event}}' } },
    },
  ],
  response: { status: 204 },
};

function renderSpecForm(spec) {
  const s = spec || {};
  renderValidationForm(s.validation);

  clear($('rules'));
  for (const rule of s.rules || []) $('rules').appendChild(ruleCard(rule));
  renumberRules();

  const def = $('default-response');
  clear(def);
  def.appendChild(responseBlock(s.response));
}

// readSpecForm throws with a readable message rather than sending something the
// server would only reject with a parse error.
function readSpecForm() {
  const spec = {};

  const validation = readValidationForm();
  if (validation) spec.validation = validation;

  const rules = Array.from($('rules').children).map(readRule);
  if (rules.length) spec.rules = rules;

  const response = readResponse($('default-response'), 'Default response');
  if (response) spec.response = response;

  return spec;
}

function loadSpecIntoEditor() {
  const ep = selectedEndpoint();
  if (!ep) return;
  renderSpecForm(ep.spec);
  setSpecDirty(false);
  hideSpecError();
}

function setSpecDirty(isDirty) {
  state.specDirty = isDirty;
  $('spec-status').textContent = isDirty ? 'unsaved changes' : '';
}

function showSpecError(message) {
  const box = $('spec-error');
  box.textContent = message;
  box.hidden = false;
}

function hideSpecError() { $('spec-error').hidden = true; }

async function saveSpec() {
  const ep = selectedEndpoint();
  if (!ep) return;

  let spec;
  try {
    spec = readSpecForm();
  } catch (e) {
    showSpecError(e.message);
    return;
  }

  try {
    const updated = await api('PUT', `${API}/endpoints/${ep.id}/spec`, JSON.stringify(spec));
    hideSpecError();
    Object.assign(ep, updated);
    loadSpecIntoEditor();
    toast('Spec saved');
  } catch (e) {
    // The endpoint keeps serving its previous spec, so nothing is broken.
    showSpecError(e.message + '\n\nThe previous spec is still serving.');
  }
}

/* ---------- import / export ---------- */

// checkKeys refuses anything the forms cannot represent. Silently dropping an
// unknown key would lose the user's data on the next save.
function checkKeys(value, allowed, label) {
  if (value === undefined) return;
  if (value === null || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`Import: ${label} must be a JSON object.`);
  }
  const unknown = Object.keys(value).filter((k) => !allowed.includes(k));
  if (unknown.length) {
    throw new Error(`Import: ${label} has unsupported key(s): ${unknown.join(', ')}.`);
  }
}

function checkImportedSpec(spec) {
  checkKeys(spec, SPEC_KEYS, 'the spec');
  checkKeys(spec.validation, VALIDATION_KEYS, 'validation');
  if (spec.validation) checkKeys(spec.validation.onFailure, RESPONSE_KEYS, 'validation.onFailure');
  checkKeys(spec.response, RESPONSE_KEYS, 'response');

  if (spec.rules !== undefined) {
    if (!Array.isArray(spec.rules)) throw new Error('Import: rules must be an array.');
    spec.rules.forEach((rule, i) => {
      checkKeys(rule, RULE_KEYS, `rules[${i}]`);
      checkKeys(rule.request, REQUEST_KEYS, `rules[${i}].request`);
      checkKeys(rule.response, RESPONSE_KEYS, `rules[${i}].response`);
    });
  }
}

// exportSpec writes out what the form currently holds, unsaved edits included.
function exportSpec() {
  const ep = selectedEndpoint();
  if (!ep) return;
  let spec;
  try {
    spec = readSpecForm();
  } catch (e) {
    showSpecError(e.message);
    return;
  }
  const url = URL.createObjectURL(new Blob([JSON.stringify(spec, null, 2)], { type: 'application/json' }));
  const a = el('a', { href: url, download: `spec-${ep.id}.json` });
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
  toast('Spec exported');
}

// importSpec fills the form but does not save: the user reviews first.
async function importSpec(file) {
  let spec;
  try {
    spec = JSON.parse(await file.text());
  } catch (e) {
    showSpecError('Import: not valid JSON — ' + e.message);
    return;
  }
  try {
    checkImportedSpec(spec);
  } catch (e) {
    showSpecError(e.message);
    return;
  }
  renderSpecForm(spec);
  setSpecDirty(true);
  hideSpecError();
  toast('Imported — review, then save');
}

/* ---------- wiring ---------- */

function switchTab(tab) {
  for (const btn of document.querySelectorAll('.tab')) {
    btn.classList.toggle('is-active', btn.dataset.tab === tab);
  }
  $('tab-requests').classList.toggle('is-active', tab === 'requests');
  $('tab-spec').classList.toggle('is-active', tab === 'spec');
}

function wire() {
  for (const btn of document.querySelectorAll('.tab')) {
    btn.addEventListener('click', () => switchTab(btn.dataset.tab));
  }

  $('create-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const name = $('create-name').value.trim();
    try {
      const created = await api('POST', `${API}/endpoints`, JSON.stringify({ name }));
      $('create-name').value = '';
      await loadEndpoints();
      await loadHealth();
      selectEndpoint(created.id);
      toast('Endpoint created');
    } catch (err) {
      toast(err.message);
    }
  });

  $('rename-endpoint').addEventListener('click', startRename);
  $('rename-cancel').addEventListener('click', stopRename);
  $('rename-form').addEventListener('submit', (e) => {
    e.preventDefault();
    saveName();
  });
  $('rename-input').addEventListener('keydown', (e) => {
    if (e.key === 'Escape') stopRename();
  });

  $('copy-url').addEventListener('click', async () => {
    const url = $('ep-url').textContent;
    try {
      await navigator.clipboard.writeText(url);
      toast('URL copied');
    } catch (e) {
      // Clipboard access can be refused; select the text so Ctrl+C works.
      const range = document.createRange();
      range.selectNodeContents($('ep-url'));
      const sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      toast('Press Ctrl+C to copy');
    }
  });

  $('delete-endpoint').addEventListener('click', async () => {
    const ep = selectedEndpoint();
    if (!ep) return;
    if (!confirm(`Delete this endpoint and everything it captured?\n\n${href(ep.url)}`)) return;
    try {
      await api('DELETE', `${API}/endpoints/${ep.id}`);
      selectEndpoint(null);
      await loadEndpoints();
      await loadHealth();
      toast('Endpoint deleted');
    } catch (e) {
      toast(e.message);
    }
  });

  for (const [id, key] of [['filter-method', 'filterMethod'], ['filter-status', 'filterStatus']]) {
    $(id).addEventListener('change', (e) => {
      state[key] = e.target.value;
      // Mark the control while it is hiding requests: an empty list under an
      // unremarkable-looking select reads as lost traffic.
      e.target.classList.toggle('is-set', e.target.value !== '');
      applyFilter();
    });
  }

  $('filter-invalid').addEventListener('change', (e) => {
    state.filterInvalid = e.target.checked;
    applyFilter();
  });

  $('reset-requests').addEventListener('click', async () => {
    const ep = selectedEndpoint();
    if (!ep) return;
    try {
      await api('POST', `${API}/endpoints/${ep.id}/reset`);
      state.lastRequestsJSON = '';
      state.expanded.clear();
      await loadRequests();
      toast('History cleared');
    } catch (e) {
      toast(e.message);
    }
  });

  $('sign-out').addEventListener('click', async () => {
    try {
      await api('POST', `${API}/auth/logout`);
    } catch (e) { /* signing out twice is not an error worth showing */ }
    requireSignIn();
  });

  $('save-spec').addEventListener('click', saveSpec);
  $('reload-spec').addEventListener('click', loadSpecIntoEditor);
  $('example-spec').addEventListener('click', () => {
    renderSpecForm(EXAMPLE);
    setSpecDirty(true);
  });

  $('add-rule').addEventListener('click', () => {
    $('rules').appendChild(ruleCard(null));
    renumberRules();
    dirty();
  });

  $('export-spec').addEventListener('click', exportSpec);
  $('import-spec').addEventListener('click', () => $('import-file').click());
  $('import-file').addEventListener('change', (e) => {
    const file = e.target.files[0];
    e.target.value = ''; // allow re-importing the same file
    if (file) importSpec(file);
  });

  $('add-v-header').addEventListener('click', () => { addHeaderRow('', ''); setSpecDirty(true); });
  $('add-v-query').addEventListener('click', () => { addQueryRow(''); setSpecDirty(true); });
  $('add-v-field').addEventListener('click', () => { addFieldRow(''); setSpecDirty(true); });

  // The remaining standalone validation controls mark the spec dirty.
  for (const id of ['v-body-contains', 'v-json-body']) {
    $(id).addEventListener('input', dirty);
    $(id).addEventListener('change', dirty);
  }


  // Ctrl/Cmd+S saves the spec from anywhere in the form.
  $('tab-spec').addEventListener('keydown', (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === 's') {
      e.preventDefault();
      saveSpec();
    }
  });

  window.addEventListener('beforeunload', (e) => {
    if (state.specDirty) {
      e.preventDefault();
      e.returnValue = '';
    }
  });
}

/* ---------- live updates ---------- */

// The console is driven by a server-sent event stream: the server says what
// changed and the console re-reads it over the ordinary API. Polling stays as
// a slow reconciliation pass — it is also what notices a session that expired
// while the stream sat idle, which EventSource has no way to report.
function startEventStream() {
  if (state.stream || !window.EventSource) return;
  let stream;
  try {
    stream = new EventSource(href(`${API}/events`));
  } catch (e) {
    return; // the poll below keeps the console working without a stream
  }
  state.stream = stream;
  stream.onopen = () => setLive(true);
  stream.onerror = () => {
    // EventSource reconnects on its own; until it does, the poll covers.
    setLive(false);
  };
  stream.onmessage = (e) => {
    let ev = null;
    try { ev = JSON.parse(e.data); } catch (err) { return; }
    onServerEvent(ev);
  };
}

function stopEventStream() {
  if (state.stream) {
    state.stream.close();
    state.stream = null;
  }
  setLive(false);
}

function setLive(live) {
  state.live = live;
  $('live').hidden = !live;
}

// onServerEvent decides how much to re-read. A deleted or created endpoint
// changes the listing; a request changes the selected endpoint's history.
function onServerEvent(ev) {
  if (!state.auth) return;
  scheduleRefresh(ev && ev.endpoint === state.selectedId &&
    (ev.type === 'request' || ev.type === 'reset'));
}

// A busy endpoint produces events far faster than the console can render them,
// so refreshes are coalesced: the first event schedules one pass and the rest
// of the burst folds into it.
let refreshPending = null;
function scheduleRefresh(withRequests) {
  if (refreshPending) {
    refreshPending.requests = refreshPending.requests || withRequests;
    return;
  }
  refreshPending = { requests: withRequests };
  setTimeout(() => {
    const want = refreshPending;
    refreshPending = null;
    refresh(want.requests).catch(() => {});
  }, COALESCE_MS);
}

// refresh re-reads whatever the console is showing. Requests are reloaded only
// when asked for: the listing alone is enough to update the counters.
async function refresh(withRequests) {
  if (!state.auth || document.hidden) return;
  await loadHealth();
  const previous = state.selectedId;
  await loadEndpoints();
  if (previous && state.selectedId === previous) {
    renderEndpointHead();
    if (withRequests) await loadRequests();
  }
}

async function poll() {
  if (document.hidden || !state.auth) return;
  await refresh(true);
}

wire();
loadAuth().then((signedIn) => {
  if (!signedIn) return;
  loadHealth();
  loadEndpoints()
    .then(startEventStream)
    .catch((e) => { if (!e.unauthorized) toast(e.message); });
});

// With the stream live the poll is only a safety net, so it runs far less
// often — but it never stops: it is the one thing that notices a lost session
// or a server restart.
let ticks = 0;
setInterval(() => {
  ticks += 1;
  if (state.live && ticks % SAFETY_TICKS !== 0) return;
  poll().catch(() => {});
}, POLL_MS);

// Nothing is refreshed while the tab is hidden, so catch up on the way back
// rather than leaving stale traffic on screen until the next tick.
document.addEventListener('visibilitychange', () => {
  if (!document.hidden) poll().catch(() => {});
});
