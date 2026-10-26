// HTTP client for the Mail API. Every call returns the decoded JSON or
// throws an ApiError whose message can be shown to the user.

export class ApiError extends Error {
  constructor(message, status) {
    super(message);
    this.status = status;
  }
}

let unauthorizedHandler = () => {};

/** Called whenever the server says the session is no longer valid. */
export function onUnauthorized(fn) {
  unauthorizedHandler = fn;
}

async function request(method, path, body, { signal } = {}) {
  const options = { method, credentials: 'same-origin', headers: {}, signal };
  if (body instanceof FormData) {
    options.body = body;
  } else if (body !== undefined) {
    options.headers['Content-Type'] = 'application/json';
    options.body = JSON.stringify(body);
  }

  let res;
  try {
    res = await fetch(path, options);
  } catch (err) {
    if (err.name === 'AbortError') throw err;
    throw new ApiError('Cannot reach the server. Check your connection.', 0);
  }

  let data = null;
  if ((res.headers.get('Content-Type') || '').includes('application/json')) {
    data = await res.json().catch(() => null);
  }
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/api/auth/')) unauthorizedHandler();
    throw new ApiError(data?.error || `Request failed (${res.status})`, res.status);
  }
  return data;
}

const get = (path, opts) => request('GET', path, undefined, opts);
const post = (path, body = {}) => request('POST', path, body);
const enc = encodeURIComponent;

function outgoing(message, files) {
  const form = new FormData();
  form.append('message', JSON.stringify(message));
  for (const f of files) form.append('files', f, f.name);
  return form;
}

export const api = {
  auth: {
    status: () => get('/api/auth/status'),
    setup: (password) => post('/api/auth/setup', { password }),
    login: (password) => post('/api/auth/login', { password }),
    logout: () => post('/api/auth/logout'),
    changePassword: (oldPassword, newPassword) =>
      post('/api/auth/change-password', { old_password: oldPassword, new_password: newPassword }),
  },

  accounts: () => get('/api/accounts'),
  folders: (account) => get(`/api/accounts/${enc(account)}/folders`),
  sync: (account = 'all') => post(`/api/accounts/${enc(account)}/sync`),
  /** Resolves once the mail changed after `since` (or after ~50 s). */
  changes: (since, signal) => get(`/api/changes?since=${since}`, { signal }),

  threads: (view, account, offset = 0, limit = 50) =>
    get(`/api/threads?view=${enc(view)}&account=${enc(account || '')}&offset=${offset}&limit=${limit}`),
  thread: (account, id, view) => get(`/api/thread?account=${enc(account)}&id=${enc(id)}&view=${enc(view)}`),
  search: (query, offset = 0, limit = 50) => get(`/api/search?q=${enc(query)}&offset=${offset}&limit=${limit}`),
  searchServer: (query) => post('/api/search/server', { q: query }),

  message: (id) => get(`/api/messages/${id}`),
  htmlURL: (id, images) => `/api/messages/${id}/html${images ? '?images=1' : ''}`,
  partURL: (id, index, download = false) => `/api/messages/${id}/parts/${index}${download ? '?download=1' : ''}`,
  allowImages: (id) => post(`/api/messages/${id}/images`),

  /** action: done | trash | inbox | read | unread | star | unstar | move | delete */
  act: (action, view, { threads = [], messages = [], folder = '' } = {}) =>
    post('/api/actions', { action, view, threads, messages, folder }),
  undo: (ids) => post('/api/undo', { ids }),

  compose: (mode, id) => get(`/api/compose?mode=${mode}&id=${id}`),
  send: (message, files) => request('POST', '/api/send', outgoing(message, files)),
  saveDraft: (message, files) => request('POST', '/api/drafts', outgoing(message, files)),
  contacts: (query) => get(`/api/contacts?q=${enc(query)}`),
};
