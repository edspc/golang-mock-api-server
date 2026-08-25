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

const state = {
  endpoints: [],
  selectedId: null,
  filterInvalid: false,
  auto: true,
  expanded: new Set(),
  lastRequestsJSON: '',
  specDirty: false,
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
  const res = await fetch(path, init);
  const text = await res.text();
  let data = null;
  if (text) {
    try { data = JSON.parse(text); } catch (e) { data = null; }
  }
  if (!res.ok) {
    throw new Error((data && data.error) || `${res.status} ${res.statusText}`);
  }
  return data;
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
  $('ep-url').textContent = location.origin + ep.url;
  $('ep-id').textContent = ep.id;
  $('ep-created').textContent = formatTime(ep.createdAt);
  $('ep-received').textContent = ep.received;
}

/* ---------- requests ---------- */

async function loadRequests() {
  const ep = selectedEndpoint();
  if (!ep) return;
  const id = ep.id;
  const filtered = state.filterInvalid;
  const query = filtered ? '?invalid=true' : '';
  let entries;
  try {
    entries = await api('GET', `${API}/endpoints/${id}/requests${query}`) || [];
  } catch (e) {
    return; // endpoint deleted mid-poll; the list refresh will notice
  }

  // The selection (or filter) can change while the request is in flight;
  // rendering a stale response would show one endpoint's traffic under
  // another's header until the next poll.
  if (state.selectedId !== id || state.filterInvalid !== filtered) return;

  const json = JSON.stringify(entries);
  if (json === state.lastRequestsJSON) return; // avoid re-rendering on every poll
  state.lastRequestsJSON = json;

  renderRequests($('requests'), entries, {
    showValidation: true,
    emptyText: filtered ? 'No failed requests captured.' : 'Nothing captured yet.',
  });
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

/* ---------- validation form ---------- */

// The validation form covers every field of the Validation struct, including
// the nested failure response. That is deliberate: a form that could not
// represent part of a saved spec would silently drop it on the next save.

// makeRow appends one removable row of text inputs to a list.
function makeRow(list, fields) {
  const inputs = fields.map((f) => {
    const input = el('input', { type: 'text', placeholder: f.placeholder, autocomplete: 'off' });
    input.value = f.value || '';
    input.addEventListener('input', () => setSpecDirty(true));
    return input;
  });
  const row = el('div', { class: 'row' });
  for (const input of inputs) row.appendChild(input);
  row.appendChild(el('button', {
    type: 'button',
    class: 'ghost row-del',
    title: 'Remove',
    text: '\u00d7',
    onclick: () => { row.remove(); setSpecDirty(true); },
  }));
  list.appendChild(row);
  return row;
}

function addHeaderRow(name, value) {
  makeRow($('v-headers'), [
    { placeholder: 'X-Signature', value: name },
    { placeholder: 'value (optional)', value: value },
  ]);
}

function addQueryRow(name) {
  makeRow($('v-query'), [{ placeholder: 'source', value: name }]);
}

function addFieldRow(path) {
  makeRow($('v-fields'), [{ placeholder: 'data.id', value: path }]);
}

function addFailHeaderRow(name, value) {
  makeRow($('v-fail-headers'), [
    { placeholder: 'Content-Type', value: name },
    { placeholder: 'application/json', value: value },
  ]);
}

// rowValues returns the trimmed inputs of each row in a list.
function rowValues(id) {
  return Array.from($(id).querySelectorAll('.row')).map((row) =>
    Array.from(row.querySelectorAll('input')).map((i) => i.value.trim()));
}

function renderValidationForm(validation) {
  const v = validation || {};
  for (const id of ['v-headers', 'v-query', 'v-fields', 'v-fail-headers']) clear($(id));

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

  const fail = v.onFailure || {};
  $('v-fail-status').value = fail.status || '';
  $('v-fail-delay').value = fail.delay || '';
  for (const [name, value] of Object.entries(fail.headers || {})) addFailHeaderRow(name, value);
  $('v-fail-body').value = fail.body === undefined ? '' : JSON.stringify(fail.body, null, 2);
  $('v-onfailure').open = !!v.onFailure;
}

// readFailureForm throws with a readable message rather than sending something
// the server would reject with a parse error.
function readFailureForm() {
  const r = {};

  const status = $('v-fail-status').value.trim();
  if (status) {
    const n = Number(status);
    if (!Number.isInteger(n) || n < 100 || n > 599) {
      throw new Error(`Failure response status must be between 100 and 599, got "${status}".`);
    }
    r.status = n;
  }

  const delay = $('v-fail-delay').value.trim();
  if (delay) r.delay = delay;

  const headers = {};
  for (const [name, value] of rowValues('v-fail-headers')) {
    if (name) headers[name] = value;
  }
  if (Object.keys(headers).length) r.headers = headers;

  const body = $('v-fail-body').value.trim();
  if (body) {
    try {
      r.body = JSON.parse(body);
    } catch (e) {
      throw new Error('Failure response body is not valid JSON: ' + e.message);
    }
  }

  return Object.keys(r).length ? r : null;
}

// readValidationForm returns null when nothing is configured, so an emptied
// form removes validation from the spec rather than saving an empty object.
function readValidationForm() {
  const v = {};

  const headers = rowValues('v-headers')
    .filter(([name]) => name)
    .map(([name, value]) => (value ? `${name}: ${value}` : name));
  if (headers.length) v.requireHeaders = headers;

  const query = rowValues('v-query').map(([name]) => name).filter(Boolean);
  if (query.length) v.requireQuery = query;

  const fields = rowValues('v-fields').map(([path]) => path).filter(Boolean);
  if (fields.length) v.requireFields = fields;

  const contains = $('v-body-contains').value.trim();
  if (contains) v.bodyContains = contains;

  if ($('v-json-body').checked) v.jsonBody = true;

  const onFailure = readFailureForm();
  if (onFailure) v.onFailure = onFailure;

  return Object.keys(v).length ? v : null;
}

/* ---------- spec ---------- */

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

// The editor holds everything except validation, which the form owns.
function rulesJSON(spec) {
  const rest = {};
  for (const [k, v] of Object.entries(spec || {})) {
    if (k !== 'validation') rest[k] = v;
  }
  return JSON.stringify(rest, null, 2);
}

function loadSpecIntoEditor() {
  const ep = selectedEndpoint();
  if (!ep) return;
  const spec = ep.spec || {};
  renderValidationForm(spec.validation);
  $('spec-editor').value = rulesJSON(spec);
  setSpecDirty(false);
  hideSpecError();
}

function setSpecDirty(dirty) {
  state.specDirty = dirty;
  $('spec-status').textContent = dirty ? 'unsaved changes' : '';
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

  let validation;
  try {
    validation = readValidationForm();
  } catch (e) {
    showSpecError(e.message);
    return;
  }

  // Catch malformed JSON here so the user gets the parse position, then let
  // the server have the final say on the contents.
  let rest;
  try {
    rest = JSON.parse($('spec-editor').value.trim() || '{}');
  } catch (e) {
    showSpecError('Rules & default response: invalid JSON — ' + e.message);
    return;
  }
  if (rest === null || typeof rest !== 'object' || Array.isArray(rest)) {
    showSpecError('Rules & default response must be a JSON object.');
    return;
  }
  if (rest.validation !== undefined) {
    showSpecError('Validation is edited in the form above. Remove "validation" from the rules JSON.');
    return;
  }

  // Unknown keys are passed through rather than dropped, so the server rejects
  // a typo with the same message it always did.
  const spec = {};
  if (validation) spec.validation = validation;
  Object.assign(spec, rest);

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
    if (!confirm(`Delete this endpoint and everything it captured?\n\n${ep.url}`)) return;
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

  $('filter-invalid').addEventListener('change', (e) => {
    state.filterInvalid = e.target.checked;
    state.lastRequestsJSON = '';
    loadRequests();
  });

  $('auto-refresh').addEventListener('change', (e) => { state.auto = e.target.checked; });

  // Refresh the received counts too, not just the request list: they come
  // from the endpoint listing and would otherwise stay stale until the poll.
  $('refresh-requests').addEventListener('click', async () => {
    state.lastRequestsJSON = '';
    await loadEndpoints();
    renderEndpointHead();
    await loadRequests();
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

  $('spec-editor').addEventListener('input', () => setSpecDirty(true));
  $('save-spec').addEventListener('click', saveSpec);
  $('reload-spec').addEventListener('click', loadSpecIntoEditor);
  $('example-spec').addEventListener('click', () => {
    renderValidationForm(EXAMPLE.validation);
    $('spec-editor').value = rulesJSON(EXAMPLE);
    setSpecDirty(true);
  });

  $('add-v-header').addEventListener('click', () => { addHeaderRow('', ''); setSpecDirty(true); });
  $('add-v-query').addEventListener('click', () => { addQueryRow(''); setSpecDirty(true); });
  $('add-v-field').addEventListener('click', () => { addFieldRow(''); setSpecDirty(true); });
  $('add-v-fail-header').addEventListener('click', () => { addFailHeaderRow('', ''); setSpecDirty(true); });

  // Every standalone validation control marks the spec dirty.
  for (const id of ['v-body-contains', 'v-json-body', 'v-fail-status', 'v-fail-delay', 'v-fail-body']) {
    $(id).addEventListener('input', () => setSpecDirty(true));
    $(id).addEventListener('change', () => setSpecDirty(true));
  }


  // Ctrl/Cmd+S saves the spec when the editor has focus.
  $('spec-editor').addEventListener('keydown', (e) => {
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

async function poll() {
  if (!state.auto || document.hidden) return;
  await loadHealth();
  const previous = state.selectedId;
  await loadEndpoints();
  if (previous && state.selectedId === previous) {
    renderEndpointHead();
    await loadRequests();
  }
}

wire();
loadHealth();
loadEndpoints().catch((e) => toast(e.message));
setInterval(() => { poll().catch(() => {}); }, POLL_MS);
