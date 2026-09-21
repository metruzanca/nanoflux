// Shared helpers for talking to the nanoflux server.

// getConfig returns the configured server URL and auth token from storage.
export async function getConfig() {
  const { server, token } = await chrome.storage.local.get(['server', 'token']);
  return { server: server || '', token: token || '' };
}

// request performs a fetch against the nanoflux server, attaching the Bearer
// token. `body` is sent as JSON for the /api endpoints.
export async function request(server, token, path, body) {
  const res = await fetch(server + path, {
    method: 'POST',
    headers: Object.assign(
      { 'Content-Type': 'application/json' },
      token ? { Authorization: 'Bearer ' + token } : {}
    ),
    body: JSON.stringify(body),
  });
  if (res.status === 401) throw new Error('unauthorized');
  if (!res.ok) throw new Error('request failed');
  return res.json();
}

// discover asks the server which feeds are on `url` and whether they're saved.
export async function discover(server, token, url) {
  return request(server, token, '/api/discover', { url });
}

// login exchanges credentials for a session token.
export async function login(server, username, password) {
  return request(server, '', '/api/login', { username, password });
}