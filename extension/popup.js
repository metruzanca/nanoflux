// Popup: finds the feeds on the current page and loads the add-feed form from
// the server with htmx. The form submits back to the server with htmx and
// swaps its result inline.
import { getConfig, discover } from './api.js';

function status(msg, ok) {
  const el = document.getElementById('state');
  el.textContent = msg;
  el.className = ok ? 'ok' : '';
}

// loadPageForm asks the server for the save-page form and swaps it into slot.
// The page URL and title are sent so the form is prefilled; the form's own
// POST goes back to /api/ext/page-save.
function loadPageForm(slot, tab) {
  slot.textContent = '';
  const h = document.createElement('div');
  h.setAttribute('hx-post', '/api/ext/page-form');
  h.setAttribute('hx-trigger', 'load');
  h.setAttribute('hx-swap', 'innerHTML');
  h.setAttribute('hx-vals', JSON.stringify({ url: tab.url, title: tab.title || '' }));
  slot.appendChild(h);
  htmx.process(slot);
}

async function main() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (!tab || !tab.url || !/^https?:/.test(tab.url)) {
    status('open a web page to find its feeds');
    return;
  }
  const cfg = await getConfig();
  if (!cfg.server || !cfg.token) {
    status('not connected — open settings');
    return;
  }

  // Relative hx-paths in the popup must resolve to the server: point the base
  // at it and attach the auth token to every htmx request.
  const base = document.createElement('base');
  base.href = cfg.server.replace(/\/+$/, '') + '/';
  document.head.appendChild(base);
  document.body.setAttribute('hx-headers', JSON.stringify({ Authorization: 'Bearer ' + cfg.token }));

  let data;
  try {
    data = await discover(cfg.server, cfg.token, tab.url);
  } catch (e) {
    status(e.message === 'unauthorized' ? 'session expired — open settings' : 'could not reach nanoflux');
    return;
  }

  // Match the app's per-user accent color.
  if (data.accent) document.documentElement.style.setProperty('--accent', data.accent);

  const slot = document.getElementById('form-slot');

  // No feed on this page: offer to save the page itself to a list.
  if (!data.candidates.length) {
    status('no feeds found — save this page instead');
    loadPageForm(slot, tab);
    return;
  }

  const saved = data.candidates.some((c) => c.saved);
  const n = data.candidates.length;
  status(`${n} feed${n === 1 ? '' : 's'} detected${saved ? ' · already saved' : ''}`, saved);

  // Already saved: link to the feed in nanoflux instead of the add form.
  if (saved) {
    const c = data.candidates.find((c) => c.saved);
    const a = document.createElement('a');
    a.href = '/feeds/' + c.saved_feed_id;
    a.target = '_blank';
    a.rel = 'noopener';
    a.textContent = 'open this feed in nanoflux';
    slot.appendChild(a);
    addPageLink(slot, tab);
    return;
  }

  // Load the add-feed form from the server via htmx.
  const h = document.createElement('div');
  h.setAttribute('hx-post', '/api/ext/feed-form');
  h.setAttribute('hx-trigger', 'load');
  h.setAttribute('hx-swap', 'innerHTML');
  h.setAttribute('hx-vals', JSON.stringify({
    url: tab.url,
    feed_url: data.candidates[0].feed_url,
    title: data.candidates[0].title || '',
  }));
  slot.appendChild(h);
  // Processing the element fires its hx-trigger="load", which loads the form.
  htmx.process(slot);
  addPageLink(slot, tab);
}

// addPageLink appends the "or save this page to a list" escape hatch, so a page
// that carries feeds can still be saved in its own right.
function addPageLink(slot, tab) {
  const alt = document.createElement('button');
  alt.type = 'button';
  alt.className = 'link alt';
  alt.textContent = 'or save this page to a list';
  alt.addEventListener('click', () => loadPageForm(slot, tab));
  slot.appendChild(alt);
}

document.getElementById('options-link').addEventListener('click', (e) => {
  e.preventDefault();
  chrome.runtime.openOptionsPage();
});

main();