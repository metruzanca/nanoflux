// nanoflux frontend helpers. htmx 2.x does not swap 4xx/5xx bodies by default;
// this override lets error fragments render so form errors are visible.
document.addEventListener('htmx:beforeSwap', function (e) {
  if (e.detail.xhr.status >= 400) e.detail.shouldSwap = true;
});

// Hidden timezone fields (the signup form) are filled from the browser so a new
// account starts with the right timezone. Server-side validation still applies.
(function () {
  var input = document.querySelector('input[type="hidden"][name="timezone"]');
  if (!input) return;
  try {
    var tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (tz) input.value = tz;
  } catch (err) {
    // No Intl support: leave it blank and let the server default apply.
  }
})();

// Theme. The server renders data-theme="dark|light|system"; "system" resolves
// against prefers-color-scheme here so the page follows the OS live.
function applyTheme(theme) {
  var html = document.documentElement;
  if (theme === 'system') {
    theme = window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }
  html.setAttribute('data-theme', theme);
}
(function () {
  if (document.documentElement.getAttribute('data-theme') !== 'system') return;
  var mq = window.matchMedia('(prefers-color-scheme: light)');
  var apply = function () { applyTheme('system'); };
  apply();
  if (mq.addEventListener) mq.addEventListener('change', apply);
  else mq.addListener(apply);
})();
document.body.addEventListener('htmx:afterRequest', function (e) {
  if (e.detail.path && e.detail.path.indexOf('/settings/theme') !== -1) {
    var checked = document.querySelector('#settings-theme-card input[name="theme"]');
    if (checked && checked.value) applyTheme(checked.value);
  }
  if (e.detail.path && e.detail.path.indexOf('/settings/accent') !== -1) {
    var input = document.querySelector('#settings-accent-card input[name="accent"]');
    if (input && input.value) applyAccent(input.value);
  }
});

// Accent color. The server renders --accent inline on <html>; this keeps it
// in sync when the setting changes via htmx without a full page load.
function applyAccent(color) {
  document.documentElement.style.setProperty('--accent', color);
}
document.addEventListener('click', function (e) {
  var swatch = e.target.closest('.accent-swatch');
  if (!swatch) return;
  var card = document.getElementById('settings-accent-card');
  if (!card) return;
  var input = card.querySelector('input[name="accent"]');
  var form = card.querySelector('form');
  if (!input || !form) return;
  input.value = swatch.dataset.accentPreset || '';
  form.requestSubmit();
});

// Picker control (shared by the display mode, authors sort, and item sort
// direction): a pill button that opens a dropdown. Client-side pickers store
// their choice locally and are re-applied after swaps; server-side pickers use
// links that swap the list.
function pickerMenu(el) {
  var ctl = el.closest('.picker');
  return ctl ? ctl.querySelector('.mode-menu') : null;
}
function togglePicker(e) {
  e.stopPropagation();
  var menu = pickerMenu(e.currentTarget);
  if (!menu) return;
  var open = menu.hidden;
  menu.hidden = !open;
  e.currentTarget.setAttribute('aria-expanded', String(!open));
}
function closePickers() {
  document.querySelectorAll('.mode-menu:not([hidden])').forEach(function (m) {
    m.hidden = true;
    var btn = m.parentElement && m.parentElement.querySelector('.mode-btn');
    if (btn) btn.setAttribute('aria-expanded', 'false');
  });
}
document.addEventListener('click', function (e) {
  document.querySelectorAll('.mode-menu:not([hidden])').forEach(function (m) {
    if (!m.contains(e.target) && !e.target.closest('.picker')) m.hidden = true;
  });
});
document.addEventListener('keydown', function (e) {
  if (e.key === 'Escape') closePickers();
});

// Set a picker's rendered state (button icon/label + aria-checked options).
function setPickerState(name, value) {
  document.querySelectorAll('.picker[data-picker="' + name + '"]').forEach(function (ctl) {
    ctl.querySelectorAll('[data-option]').forEach(function (el) {
      var on = el.dataset.option === value;
      if (el.classList.contains('mode-option')) el.setAttribute('aria-checked', String(on));
      else el.hidden = !on;
    });
  });
}

// Client-side picker options (display mode, authors sort) carry no inline
// handler (templ can't express one); a delegated click maps the picker name +
// option value to the right setter.
document.addEventListener('click', function (e) {
  var opt = e.target.closest('.picker .mode-option[data-option]');
  if (!opt || opt.tagName !== 'BUTTON') return;
  var ctl = opt.closest('.picker');
  var name = ctl && ctl.dataset.picker;
  var value = opt.dataset.option;
  if (name === 'display') setDisplayMode(value, e);
  else if (name === 'authors') setAuthorSort(value, e);
});

// Display mode (list / masonry grid). Stored server-side per account, per
// scope (the picker carries the page key in data-scope and the saved mode in
// data-mode), so the choice follows the user across devices. The server also
// renders the list with the right class, so this mostly re-syncs the picker
// after an htmx swap recreates it.
function displayScope() {
  var ctl = document.querySelector('.picker[data-picker="display"]');
  return (ctl && ctl.dataset.scope) || '';
}
function displayMode() {
  var ctl = document.querySelector('.picker[data-picker="display"]');
  return (ctl && ctl.dataset.mode === 'grid') ? 'grid' : 'list';
}
function applyDisplayMode() {
  var mode = displayMode();
  setPickerState('display', mode);
}
function setDisplayMode(mode, e) {
  if (e) e.stopPropagation();
  var scope = displayScope();
  closePickers();
  // Apply locally right away, then persist. A failed save leaves the page on
  // the new mode until the next load, which is preferable to a visible jank.
  document.querySelectorAll('.picker[data-picker="display"]').forEach(function (ctl) {
    ctl.dataset.mode = mode;
  });
  var list = document.getElementById('items-list');
  if (list && list.tagName === 'UL') {
    list.classList.toggle('masonry', mode === 'grid');
  }
  setPickerState('display', mode);
  fetch('/prefs/display', {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: 'scope=' + encodeURIComponent(scope) + '&mode=' + encodeURIComponent(mode)
  }).catch(function () {});
}

// Authors sort (abc / newest / unread). Client-side (localStorage), reorders
// the rendered rows. The default is "unread".
var AUTHOR_SORT_KEY = 'nanoflux.authors.sort';
function authorSort() {
  var s = localStorage.getItem(AUTHOR_SORT_KEY);
  if (s === 'abc' || s === 'newest') return s;
  return 'unread';
}
function applyAuthorSort() {
  var sort = authorSort();
  var list = document.getElementById('authors-list');
  if (list) {
    var rows = Array.prototype.slice.call(list.children);
    rows.sort(function (a, b) {
      if (sort === 'newest') {
        return (b.dataset.created || '').localeCompare(a.dataset.created || '');
      }
      if (sort === 'unread') {
        return (parseInt(b.dataset.unread, 10) || 0) - (parseInt(a.dataset.unread, 10) || 0);
      }
      return (a.dataset.name || '').localeCompare(b.dataset.name || '');
    });
    rows.forEach(function (r) { list.appendChild(r); });
  }
  setPickerState('authors', sort);
}
function setAuthorSort(sort, e) {
  if (e) e.stopPropagation();
  localStorage.setItem(AUTHOR_SORT_KEY, sort);
  applyAuthorSort();
  closePickers();
}

applyDisplayMode();
applyAuthorSort();

// Author edit form: clone the blank link row <template> to add another.
function addAuthorLinkRow() {
  var tpl = document.getElementById('author-link-row-template');
  var list = document.getElementById('author-link-fields');
  if (!tpl || !list) return;
  list.appendChild(tpl.content.firstElementChild.cloneNode(true));
}

// Item modal.
var currentItemId = null;
var itemDialog = document.getElementById('item-dialog');

// The open item is mirrored in the URL hash (#item-<id>) so the modal deep-links
// and opens in a new tab, and browser back/forward move between items.
function hashItemId() {
  var m = /^#item-(\d+)$/.exec(location.hash);
  return m ? m[1] : null;
}
function syncItemHash(id, wasOpen) {
  var target = '#item-' + id;
  if (location.hash === target) return;
  // A deep link already owns the entry; only push on a fresh open, and replace
  // when moving between items inside an already-open modal.
  if (wasOpen) history.replaceState({ item: id }, '', target);
  else history.pushState({ item: id }, '', target);
}
function openItem(el) {
  return openItemData(el.dataset.itemId);
}
function openItemData(id) {
  if (!itemDialog) return;
  var wasOpen = itemDialog.open;
  currentItemId = id;
  var body = document.getElementById('item-dialog-body');
  body.innerHTML = '<p class="muted">loading…</p>';
  var slot = document.getElementById('item-dialog-controls');
  if (slot) slot.replaceChildren();
  fetch('/items/' + id + '/view')
    .then(function (r) { return r.text(); })
    .then(function (html) {
      body.innerHTML = html;
      // The item fragment renders the ⋯ (and share) controls inside the
      // scrollable body; relocate them into the dialog header next to ✕.
      var controls = body.querySelector('#item-dialog-controls-src');
      if (controls && slot) slot.replaceChildren(controls);
      // The modal is injected via plain innerHTML, so htmx never processed its
      // elements (e.g. the share button's hx-post). Initialize them here.
      htmx.process(document.getElementById('item-dialog'));
      // Focus the scrollable body so the browser's arrow keys scroll the post
      // (rather than the dialog's first button grabbing focus).
      body.focus();
      markRowRead(id);
    })
    .catch(function () { body.innerHTML = '<p class="error">could not load item</p>'; });
  itemDialog.showModal();
  syncItemHash(id, wasOpen);
  return false;
}
// openItemById finds an item's rendered row on the page and opens it; when the
// item isn't in the current list (a deep link to an item on another page) the
// modal still opens from the id alone.
function openItemById(id) {
  var row = document.getElementById('item-' + id);
  var anchor = row && row.querySelector('[data-item-id]');
  if (anchor) { openItem(anchor); return; }
  openItemData(id);
}

if (itemDialog) {
  // Opening the modal pushes #item-<id>; closing strips it again (replaceState,
  // not a navigation) so the URL doesn't keep a stale hash.
  itemDialog.addEventListener('close', function () {
    if (location.hash) history.replaceState(null, '', location.pathname + location.search);
    currentItemId = null;
  });
  // The item dialog closes only via Esc (native) or the ✕ button. A click on
  // the backdrop deliberately does NOT close it: an accidental outside click
  // shouldn't dismiss the item (and would stop embedded video playback).
}

// Browser back/forward reconciles the modal with the hash.
window.addEventListener('popstate', function () {
  if (!itemDialog) return;
  var id = hashItemId();
  if (id) {
    if (!itemDialog.open || String(currentItemId) !== id) openItemById(id);
  } else if (itemDialog.open) {
    itemDialog.close();
  }
});

// A page loaded with #item-<id> opens that item's modal.
document.addEventListener('DOMContentLoaded', function () {
  var id = hashItemId();
  if (id) openItemById(id);
});

function markRowRead(id) {
  var row = document.getElementById('item-' + id);
  if (!row) return;
  var title = row.querySelector('.item-title');
  if (title) title.classList.remove('unread');
  var btn = row.querySelector('.read-btn');
  if (btn) {
    btn.textContent = '↺';
    btn.setAttribute('title', 'mark unread');
  }
}

// Item "⋯" menu on cards (list/grid) and in the item modal. Each menu is
// scoped per item via data-item-id, so many cards can each have one; the
// "add to list" entry fetches the shared #item-lists-dialog picker.
function openItemMenus() {
  return Array.prototype.slice.call(document.querySelectorAll('.item-menu .menu-pop'));
}
function closeItemMenus() {
  openItemMenus().forEach(function (menu) {
    menu.hidden = true;
    var ctl = menu.closest('.item-menu');
    if (!ctl) return;
    var btn = ctl.querySelector('.menu-btn');
    if (btn) btn.setAttribute('aria-expanded', 'false');
    var li = ctl.closest('li');
    if (li) li.classList.remove('menu-open');
  });
}
function toggleItemMenu(e) {
  e.stopPropagation();
  var ctl = e.currentTarget.closest('.item-menu');
  if (!ctl) return;
  closeItemMenus();
  var menu = ctl.querySelector('.menu-pop');
  var open = !menu.hidden;
  menu.hidden = open;
  e.currentTarget.setAttribute('aria-expanded', String(!open));
  // Let the dropdown escape the row's overflow:hidden (the swipe container)
  // while it's open.
  var li = ctl.closest('li');
  if (li) li.classList.toggle('menu-open', !open);
}
function itemMenuAction(e, action) {
  e.stopPropagation();
  var ctl = e.currentTarget.closest('.item-menu');
  if (!ctl) return;
  closeItemMenus();
  if (action !== 'lists') return;
  var d = document.getElementById('item-lists-dialog');
  if (!d) return;
  d.innerHTML = '<p class="muted">loading…</p>';
  d.showModal();
  fetch('/items/' + ctl.dataset.itemId + '/lists')
    .then(function (r) { return r.text(); })
    .then(function (html) {
      d.innerHTML = html;
      // The picker's form carries hx attributes; it was injected via innerHTML,
      // so initialize it (and its combo box, which no htmx event announced).
      htmx.process(d);
      if (window.nanofluxReinitVaadin) window.nanofluxReinitVaadin(d);
    })
    .catch(function () { d.innerHTML = '<p class="error">could not load lists</p>'; });
}
document.addEventListener('click', function (e) {
  if (e.target.closest && e.target.closest('.item-menu')) return;
  closeItemMenus();
});
// The add-to-list dialog's form is swapped out of its own container on save,
// so its inline after-request close never runs. The server signals success with
// HX-Trigger: item-lists-saved, which htmx dispatches on body; close the dialog
// here. The response has already swapped fresh membership state in.
document.body.addEventListener('item-lists-saved', function () {
  var d = document.getElementById('item-lists-dialog');
  if (d && d.open) d.close();
});
document.addEventListener('keydown', function (e) {
  if (e.key === 'Escape') closeItemMenus();
});

// User dropdown menu.
function toggleUserMenu(e) {
  e.stopPropagation();
  var menu = document.getElementById('user-menu');
  var btn = document.getElementById('user-menu-btn');
  var open = menu.hidden;
  menu.hidden = !open;
  if (btn) btn.setAttribute('aria-expanded', String(open));
}
document.addEventListener('click', function (e) {
  var menu = document.getElementById('user-menu');
  var btn = document.getElementById('user-menu-btn');
  if (!menu || menu.hidden) return;
  if (!e.target.closest('.user-menu')) {
    menu.hidden = true;
    if (btn) btn.setAttribute('aria-expanded', 'false');
  }
});
document.addEventListener('keydown', function (e) {
  if (e.key === 'Escape') {
    var menu = document.getElementById('user-menu');
    var btn = document.getElementById('user-menu-btn');
    if (menu && !menu.hidden) {
      menu.hidden = true;
      if (btn) btn.setAttribute('aria-expanded', 'false');
    }
  }
});

// Mobile nav (hamburger). The nav links collapse behind a toggle on narrow
// screens; it closes on outside click, Escape, or when a link is tapped.
function toggleNav(e) {
  if (e) e.stopPropagation();
  var nav = document.getElementById('top-nav');
  var btn = document.getElementById('nav-toggle');
  if (!nav) return;
  var open = nav.classList.toggle('open');
  if (btn) btn.setAttribute('aria-expanded', String(open));
}
function closeNav() {
  var nav = document.getElementById('top-nav');
  var btn = document.getElementById('nav-toggle');
  if (!nav) return;
  nav.classList.remove('open');
  if (btn) btn.setAttribute('aria-expanded', 'false');
}
document.addEventListener('click', function (e) {
  var nav = document.getElementById('top-nav');
  if (!nav || !nav.classList.contains('open')) return;
  if (e.target.closest('.hamburger')) return;
  if (!e.target.closest('.top')) closeNav();
});
document.addEventListener('click', function (e) {
  var nav = document.getElementById('top-nav');
  if (!nav || !nav.classList.contains('open')) return;
  if (nav.contains(e.target) && e.target.closest('a, form')) closeNav();
});
document.addEventListener('keydown', function (e) {
  if (e.key === 'Escape') closeNav();
});

// Arrow keys move through the item list while the modal is open. Reaching the
// end of the loaded rows fetches the next page (the "load more" cursor) and
// continues into it, so reading never dead-ends. The same mechanism serves
// every paged list (unread, read, author, feed, collection, favorites, lists).
var loadMoreBusy = false;
var continueAfterLoad = false;

function loadMoreButton() {
  return document.getElementById('load-more');
}
// requestMore clicks the htmx "load more" button if a page is still available
// and no request is already in flight. Returns whether it fired one.
function requestMore() {
  var btn = loadMoreButton();
  if (!btn || loadMoreBusy) return false;
  loadMoreBusy = true;
  btn.click();
  return true;
}
// navigateItem opens the row dir steps from the current item (dir 1 = next,
// -1 = previous). Returns false at the edge of the loaded rows.
function navigateItem(dir) {
  var current = document.getElementById('item-' + currentItemId);
  if (!current) return false;
  var list = current.closest('ul.items') || current.parentElement;
  var items = Array.prototype.slice.call(list.querySelectorAll('li[id^="item-"]'));
  var next = items[items.indexOf(current) + dir];
  if (!next) return false;
  var link = next.querySelector('[data-item-id]');
  if (!link) return false;
  openItem(link);
  return true;
}
// prefetchNearEnd loads the next page once the current item is one row from the
// end, so the following press is instant ("1 post before" buffer).
function prefetchNearEnd() {
  var current = document.getElementById('item-' + currentItemId);
  if (!current) return;
  var list = current.closest('ul.items') || current.parentElement;
  var items = list.querySelectorAll('li[id^="item-"]');
  if (Array.prototype.indexOf.call(items, current) >= items.length - 2) requestMore();
}

document.addEventListener('keydown', function (e) {
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
  var dialog = document.getElementById('item-dialog');
  if (!dialog || !dialog.open || !currentItemId) return;
  if (e.target.closest && e.target.closest('input, textarea, select')) return;
  var dir = e.key === 'ArrowRight' ? 1 : -1;

  if (navigateItem(dir)) {
    e.preventDefault();
    if (dir > 0) prefetchNearEnd();
    return;
  }
  // At the end of the loaded rows: if more pages exist, page forward and
  // continue into the new rows once they arrive (requestMore is a no-op when a
  // prefetch is already in flight, so the flag just waits for it).
  if (dir > 0 && loadMoreButton()) {
    continueAfterLoad = true;
    requestMore();
    e.preventDefault();
  }
});

// "/" focuses the search box.
document.addEventListener('keydown', function (e) {
  if (e.key !== '/' || e.metaKey || e.ctrlKey || e.altKey) return;
  var t = e.target;
  if (t && t.closest && t.closest('input, textarea, select, [contenteditable="true"]')) return;
  var input = document.getElementById('search-input');
  if (!input) return;
  e.preventDefault();
  input.focus();
  input.select();
});

// Command palette (ctrl/cmd+p) and unified entity search (ctrl/cmd+shift+p).
// The palette engine renders a filtered, keyboard-navigable list into a
// <dialog class="palette">; Enter activates the highlighted row, Escape and
// backdrop-click close it (Escape natively, backdrop via the handler below).
var SIGILS = { author: '@', collection: '#', feed: '!' };

function paletteItemEl(row) {
  var li = document.createElement('li');
  li.className = 'palette-item';
  li.setAttribute('role', 'option');
  if (row.sigil) {
    var s = document.createElement('span');
    s.className = 'palette-sigil sigil-' + row.kind;
    s.textContent = row.sigil;
    li.appendChild(s);
  }
  var name = document.createElement('span');
  name.className = 'palette-name';
  name.textContent = row.label;
  li.appendChild(name);
  if (row.hint) {
    var hint = document.createElement('span');
    hint.className = 'palette-hint';
    hint.textContent = row.hint;
    li.appendChild(hint);
  }
  return li;
}

// wirePalette binds an input + result list into a keyboard-navigable dropdown.
// items() returns the full row set; render() is called on every change and
// receives the filtered rows. Rows are plain objects; onEnter(row) runs the
// activation.
function wirePalette(dialogId, inputId, listId, items, onEnter) {
  var dialog = document.getElementById(dialogId);
  var input = document.getElementById(inputId);
  var list = document.getElementById(listId);
  if (!dialog || !input || !list) return null;
  var shown = [];
  var active = 0;

  function render() {
    shown = items(input.value);
    list.innerHTML = '';
    if (!shown.length) {
      var empty = document.createElement('li');
      empty.className = 'palette-empty muted';
      empty.textContent = 'no matches';
      list.appendChild(empty);
      return;
    }
    if (active >= shown.length) active = shown.length - 1;
    shown.forEach(function (row, i) {
      var el = paletteItemEl(row);
      if (i === active) el.classList.add('active');
      el.addEventListener('mousemove', function () { active = i; paint(); });
      el.addEventListener('click', function () { activate(i); });
      list.appendChild(el);
    });
    paint();
  }
  function paint() {
    Array.prototype.forEach.call(list.children, function (el, i) {
      el.classList.toggle('active', i === active);
    });
  }
  function activate(i) {
    var row = shown[i];
    if (!row) return;
    close();
    onEnter(row);
  }
  function open() {
    if (dialog.open) return;
    active = 0;
    input.value = '';
    render();
    dialog.showModal();
    input.focus();
  }
  function close() {
    if (dialog.open) dialog.close();
  }

  input.addEventListener('input', function () { active = 0; render(); });
  input.addEventListener('keydown', function (e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); if (active < shown.length - 1) { active++; paint(); } }
    else if (e.key === 'ArrowUp') { e.preventDefault(); if (active > 0) { active--; paint(); } }
    else if (e.key === 'Enter') { e.preventDefault(); activate(active); }
  });
  // Backdrop click closes (the click target is the dialog itself). Escape is
  // handled natively by <dialog>.
  dialog.addEventListener('click', function (e) { if (e.target === dialog) close(); });
  return { open: open, close: close };
}

// Command palette rows: navigation plus a few common actions. Admin-only rows
// are gated on body[data-admin].
function commandRows() {
  var admin = document.body.dataset.admin === 'true';
  var run = function (path) { return function () { window.location.href = path; }; };
  var openDialog = function (path, dialogId) {
    return function () {
      try { sessionStorage.setItem('nanoflux.pendingDialog', dialogId); } catch (err) {}
      window.location.href = path;
    };
  };
  var rows = [
    { label: 'home', hint: 'go', run: run('/') },
    { label: 'unread', hint: 'go', run: run('/unread') },
    { label: 'history', hint: 'go', run: run('/read') },
    { label: 'favorites', hint: 'go', run: run('/favorites') },
    { label: 'authors', hint: 'go', run: run('/authors') },
    { label: 'collections', hint: 'go', run: run('/collections') },
    { label: 'lists', hint: 'go', run: run('/lists') },
    { label: 'settings', hint: 'go', run: run('/settings') },
    { label: 'add author', hint: 'action', run: openDialog('/authors', 'add-author-dialog') },
    { label: 'new collection', hint: 'action', run: openDialog('/collections', 'add-collection-dialog') },
    { label: 'new list', hint: 'action', run: openDialog('/lists', 'add-list-dialog') },
    { label: 'export opml', hint: 'action', run: function () { window.location.href = '/settings/export.opml'; } },
    { label: 'mark all read', hint: 'action', run: function () { postAndReload('/items/read-all'); } },
    { label: 'log out', hint: 'action', run: function () { postForm('/logout'); } },
  ];
  if (admin) {
    rows.push({ label: 'admin', hint: 'go', run: run('/admin') });
  }
  return rows;
}
function filterCommands(q) {
  q = q.trim().toLowerCase();
  return commandRows().filter(function (r) { return !q || r.label.toLowerCase().indexOf(q) !== -1; });
}

// postAndReload submits an htmx-style POST (the server reads no body) then
// reloads; used for "mark all read".
function postAndReload(path) {
  fetch(path, { method: 'POST', credentials: 'same-origin' }).finally(function () {
    window.location.reload();
  });
}

// markUnreadReload strips the item hash before reloading, so the reopened page
// doesn't auto-open the modal (which would immediately re-mark the item read).
function markUnreadReload() {
  history.replaceState(null, '', location.pathname + location.search);
  window.location.reload();
}
function postForm(path) {
  var f = document.createElement('form');
  f.method = 'POST';
  f.action = path;
  document.body.appendChild(f);
  f.submit();
}

var commandPalette = wirePalette('command-palette', 'command-input', 'command-results',
  filterCommands, function (row) { row.run(); });

// Unified entity search. Entities are fetched when the palette opens (and
// cached for the session); a leading @/#/! narrows to one kind without the
// sigil counting as part of the query.
var ENTITY_CACHE = null;
function fetchEntities() {
  return fetch('/api/entities', { credentials: 'same-origin' })
    .then(function (r) { return r.ok ? r.json() : { entities: [] }; })
    .then(function (d) { ENTITY_CACHE = d.entities || []; return ENTITY_CACHE; })
    .catch(function () { ENTITY_CACHE = []; return ENTITY_CACHE; });
}
function filterEntities(q) {
  var entities = ENTITY_CACHE || [];
  q = q.trim();
  var kind = null;
  var first = q.charAt(0);
  if (first === '@') { kind = 'author'; q = q.slice(1).trim(); }
  else if (first === '#') { kind = 'collection'; q = q.slice(1).trim(); }
  else if (first === '!') { kind = 'feed'; q = q.slice(1).trim(); }
  var needle = q.toLowerCase();
  return entities.filter(function (e) {
    if (kind && e.kind !== kind) return false;
    return !needle || e.name.toLowerCase().indexOf(needle) !== -1;
  }).map(function (e) {
    return {
      kind: e.kind, sigil: SIGILS[e.kind], label: e.name,
      hint: e.kind, url: e.url,
    };
  });
}
var entityPalette = wirePalette('entity-palette', 'entity-input', 'entity-results',
  filterEntities, function (row) { window.location.href = row.url; });
if (entityPalette) {
  var openEntity = entityPalette.open;
  entityPalette.open = function () {
    fetchEntities().then(function () { openEntity(); });
  };
}

document.addEventListener('keydown', function (e) {
  if (e.code !== 'KeyP' || !(e.ctrlKey || e.metaKey)) return;
  e.preventDefault();
  if (e.shiftKey) { if (entityPalette) entityPalette.open(); }
  else if (commandPalette) commandPalette.open();
});

// A command that opens a page dialog ("add author", ...) sets a pendingDialog
// marker before navigating; open it here once the destination loads.
document.addEventListener('DOMContentLoaded', function () {
  var pending;
  try { pending = sessionStorage.getItem('nanoflux.pendingDialog'); } catch (err) { return; }
  if (!pending) return;
  try { sessionStorage.removeItem('nanoflux.pendingDialog'); } catch (err) {}
  var d = document.getElementById(pending);
  if (d) d.showModal();
});

// Keyboard shortcuts: j/k move a row cursor, o/Enter open, v opens the
// original, s toggles favorite, m toggles read, g/G jump to first/last,
// ? shows the shortcut sheet.
var currentRow = null;
var activeItemId = null;

function itemRows() {
  var list = document.querySelector('ul.items');
  return list ? Array.prototype.slice.call(list.querySelectorAll('li[id^="item-"]')) : [];
}
function setActiveRow(row) {
  if (currentRow) currentRow.classList.remove('active-row');
  currentRow = row;
  activeItemId = row ? row.id : null;
  if (row) row.classList.add('active-row');
}
function activeRow() {
  if (currentRow && currentRow.isConnected) return currentRow;
  if (activeItemId) {
    var r = document.getElementById(activeItemId);
    if (r) { currentRow = r; r.classList.add('active-row'); return r; }
  }
  return null;
}
function openRow(row) {
  var link = row.querySelector('[data-item-id]');
  if (!link) return false;
  link.click();
  return true;
}
function toggleHelp() {
  var d = document.getElementById('shortcuts-dialog');
  if (!d) return;
  if (d.open) d.close();
  else d.showModal();
}

document.addEventListener('keydown', function (e) {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  var t = e.target;
  if (t && t.closest && t.closest('input, textarea, select, [contenteditable="true"]')) return;
  var rows = itemRows();
  if (!rows.length) return;
  var dialog = document.getElementById('item-dialog');
  var inDialog = dialog && dialog.open;

  var move = function (dir) {
    e.preventDefault();
    var idx = rows.indexOf(activeRow());
    var next = idx === -1 ? (dir > 0 ? 0 : rows.length - 1) : idx + dir;
    if (next < 0 || next >= rows.length) return;
    var row = rows[next];
    setActiveRow(row);
    if (inDialog) openRow(row);
  };

  switch (e.key) {
    case 'j':
      move(1);
      break;
    case 'k':
      move(-1);
      break;
    case 'ArrowDown':
      // While the item modal is open, leave the arrows to the browser so they
      // scroll the post's body; j/k still move between items.
      if (!inDialog) move(1);
      break;
    case 'ArrowUp':
      if (!inDialog) move(-1);
      break;
    case 'g':
      if (!inDialog) { e.preventDefault(); setActiveRow(rows[0]); }
      break;
    case 'G':
      if (!inDialog) { e.preventDefault(); setActiveRow(rows[rows.length - 1]); }
      break;
    case 'o':
      if (activeRow()) { e.preventDefault(); openRow(activeRow()); }
      break;
    case 'Enter':
      // Leave Enter alone when a control is focused so buttons still work.
      if (t && t.closest && t.closest('button, a, input, textarea, select')) return;
      if (activeRow()) { e.preventDefault(); openRow(activeRow()); }
      break;
    case 'v':
      if (activeRow()) {
        var a = activeRow().querySelector('[data-item-link]');
        if (a && a.dataset.itemLink) {
          e.preventDefault();
          window.open(a.dataset.itemLink, '_blank', 'noopener');
        }
      }
      break;
    case 's':
      if (activeRow()) {
        var fav = activeRow().querySelector('.fav-btn');
        if (fav) { e.preventDefault(); fav.click(); }
      }
      break;
    case 'm':
      if (activeRow()) {
        var rd = activeRow().querySelector('.read-btn');
        if (rd) { e.preventDefault(); rd.click(); }
      }
      break;
    case '?':
      e.preventDefault();
      toggleHelp();
      break;
  }
});

// Keep the row cursor highlighted when htmx re-renders the row (favorite/read
// toggles swap it via outerHTML), and re-apply the client-side pickers after any
// swap that recreates a list (tab switch, mark all read, load-more, author add).
document.body.addEventListener('htmx:afterSwap', function (e) {
  applyDisplayMode();
  applyAuthorSort();
  if (activeItemId) {
    var r = document.getElementById(activeItemId);
    if (r) r.classList.add('active-row');
  }
  // A "load more" page arrived into the list: release the guard and, if the
  // reader reached the end to fetch it, advance into the newly appended rows.
  if (loadMoreBusy && e.detail && e.detail.target && e.detail.target.id === 'items-list') {
    loadMoreBusy = false;
    if (continueAfterLoad) {
      continueAfterLoad = false;
      navigateItem(1);
      prefetchNearEnd();
    }
  }
});
// A failed load-more leaves no rows to advance into; release the guard so the
// reader can retry (the button is still there while a page remains).
document.body.addEventListener('htmx:responseError', function () {
  loadMoreBusy = false;
  continueAfterLoad = false;
});

// Tinder-style swipe actions on item rows (touch only). Swiping right toggles
// favorite, left toggles read, by clicking the row's existing buttons so the
// htmx swap and error handling are reused. touch-action: pan-y keeps vertical
// scrolling native; the touchmove handler preventDefaults once a horizontal
// swipe is detected so the browser never treats it as a scroll/pan (which on
// Android can fire touchcancel and kill the gesture). A data-suppress marker
// swallows any leftover synthetic click so a swipe can't open the modal.
(function () {
  var SWIPE_THRESHOLD = 12;   // px of horizontal travel before it's a swipe
  var COMMIT_RATIO = 0.4;     // fraction of the row width to commit
  var COMMIT_MIN = 80;        // px floor for committing a swipe
  var SETTLE_MS = 200;        // animation before clicking the action button

  var row = null;
  var startX = 0;
  var startY = 0;
  var swiping = false;

  function reset() {
    if (row) {
      row.classList.remove('swiping-right', 'swiping-left', 'dragging');
      row.style.removeProperty('--swipe-x');
    }
    row = null;
    swiping = false;
  }

  document.addEventListener('touchstart', function (e) {
    if (row) return;
    var t = e.changedTouches[0];
    var r = t.target.closest && t.target.closest('li[id^="item-"]');
    if (!r) return;
    row = r;
    startX = t.clientX;
    startY = t.clientY;
    swiping = false;
  }, { passive: true });

  document.addEventListener('touchmove', function (e) {
    if (!row) return;
    var t = e.changedTouches[0];
    var dx = t.clientX - startX;
    var dy = t.clientY - startY;
    if (!swiping) {
      if (Math.abs(dx) < SWIPE_THRESHOLD || Math.abs(dx) <= Math.abs(dy)) return;
      swiping = true;
      // Disable the CSS transition on the row's children so the card tracks
      // the finger 1:1 instead of easing toward it on every move.
      row.classList.add('dragging');
    }
    // Stop the browser from scrolling/pulling-to-refresh once it's horizontal.
    e.preventDefault();
    var width = row.offsetWidth;
    var clamped = Math.max(-width * 0.8, Math.min(width * 0.8, dx));
    row.style.setProperty('--swipe-x', clamped + 'px');
    row.classList.toggle('swiping-right', clamped > 0);
    row.classList.toggle('swiping-left', clamped < 0);
  }, { passive: false });

  function settle(e) {
    if (!row) return;
    var t = e.changedTouches[0];
    var dx = t.clientX - startX;
    if (!swiping) { reset(); return; }
    var commit = Math.abs(dx) >= Math.min(row.offsetWidth * COMMIT_RATIO, COMMIT_MIN);
    if (!commit) { reset(); return; }
    var dir = dx > 0 ? 1 : -1;
    // Guard against a synthetic click sneaking through on this touch.
    row.setAttribute('data-suppress', '1');
    // Re-enable the transition so the slide-off (and snap-back) animate.
    row.classList.remove('dragging');
    row.style.setProperty('--swipe-x', dir * row.offsetWidth + 'px');
    setTimeout(function () {
      var r = row;
      var fav = r.querySelector('.fav-btn');
      var read = r.querySelector('.read-btn');
      // Leave --swipe-x in place so the row stays slid off until the htmx
      // outerHTML swap replaces it with the fresh row.
      row = null;
      swiping = false;
      r.removeAttribute('data-suppress');
      r.classList.remove('swiping-right', 'swiping-left', 'dragging');
      if (dir > 0) { if (fav) fav.click(); } else { if (read) read.click(); }
    }, SETTLE_MS);
  }

  document.addEventListener('touchend', settle);
  document.addEventListener('touchcancel', reset);
})();

// Swallow any synthetic click on a row that just got swiped (the button clicks
// fire after data-suppress is cleared, so htmx actions are unaffected).
document.addEventListener('click', function (e) {
  var r = e.target.closest && e.target.closest('[data-suppress]');
  if (!r) return;
  e.preventDefault();
  e.stopPropagation();
  r.removeAttribute('data-suppress');
}, true);

// When any modal dialog closes, reset its form and clear the feed/author
// preview container so stale state doesn't leak into the next open.
document.addEventListener('close', function (e) {
  var dialog = e.target;
  if (!(dialog instanceof HTMLDialogElement)) return;
  dialog.querySelectorAll('input, textarea').forEach(function (el) {
    if (el.type === 'hidden') return;
    el.value = '';
  });
  dialog.querySelectorAll('[id$="-preview"]').forEach(function (el) {
    el.innerHTML = '';
  });
}, true);
// Installable PWA. Register the (minimal, non-caching) service worker so the
// browser offers "Add to Home screen"/"Install app". Only runs in a secure
// context (https or localhost), which is required for service workers.
if ('serviceWorker' in navigator && window.isSecureContext) {
  window.addEventListener('load', function () {
    navigator.serviceWorker.register('/sw.js').catch(function () {
      // Non-fatal: the app works fine without the worker.
    });
  });
}
