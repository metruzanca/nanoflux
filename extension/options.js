// Options: configure the nanoflux server and log in to obtain a session token.
import { getConfig, login } from './api.js';

const form = document.getElementById('opts');
const status = document.getElementById('status');

async function init() {
  const cfg = await getConfig();
  if (cfg.server) form.server.value = cfg.server;
}

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  status.textContent = '';
  status.className = 'error';
  const server = form.server.value.trim().replace(/\/+$/, '');
  if (!server) {
    status.textContent = 'server url required';
    return;
  }
  try {
    const data = await login(server, form.username.value, form.password.value);
    await chrome.storage.local.set({ server, username: form.username.value, token: data.token });
    status.className = 'ok';
    status.textContent = 'connected — token stored';
  } catch (err) {
    status.textContent = err.message === 'request failed' ? 'login failed — check credentials' : 'could not reach server';
  }
});

init();