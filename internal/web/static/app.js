// nanoflux frontend helpers. htmx 2.x does not swap 4xx/5xx bodies by default;
// this override lets error fragments render so form errors are visible.
document.addEventListener('htmx:beforeSwap', function (e) {
  if (e.detail.xhr.status >= 400) e.detail.shouldSwap = true;
});

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
    var checked = document.querySelector('#settings-theme-card input[name="theme"]:checked');
    if (checked) applyTheme(checked.value);
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

// Display mode (list / masonry grid). Client-side (localStorage), applied to
// whatever item list is on the page; htmx swaps re-create the list, so
// applyDisplayMode re-runs on every afterSwap.
var DISPLAY_MODE_KEY = 'nanoflux.items.mode';
function displayMode() {
  var m = localStorage.getItem(DISPLAY_MODE_KEY);
  return m === 'grid' ? 'grid' : 'list';
}
function applyDisplayMode() {
  var mode = displayMode();
  var list = document.getElementById('items-list');
  if (list && list.tagName === 'UL') {
    list.classList.toggle('masonry', mode === 'grid');
  }
  setPickerState('display', mode);
}
function setDisplayMode(mode, e) {
  if (e) e.stopPropagation();
  localStorage.setItem(DISPLAY_MODE_KEY, mode);
  applyDisplayMode();
  closePickers();
}

// Authors sort (abc / newest / unread). Client-side (localStorage), reorders
// the rendered rows.
var AUTHOR_SORT_KEY = 'nanoflux.authors.sort';
function authorSort() {
  var s = localStorage.getItem(AUTHOR_SORT_KEY);
  if (s === 'newest' || s === 'unread') return s;
  return 'abc';
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
function openItem(el) {
  currentItemId = el.dataset.itemId;
  document.getElementById('item-dialog-live').href = el.dataset.itemLink;
  var body = document.getElementById('item-dialog-body');
  body.innerHTML = '<p class="muted">loading…</p>';
  var slot = document.getElementById('item-dialog-controls');
  if (slot) slot.replaceChildren();
  fetch('/items/' + el.dataset.itemId + '/view')
    .then(function (r) { return r.text(); })
    .then(function (html) {
      body.innerHTML = html;
      // The item fragment renders the share + ⋯ controls inside the scrollable
      // body; relocate them into the dialog header next to "open live" and ✕.
      var controls = body.querySelector('#item-dialog-controls-src');
      if (controls && slot) slot.replaceChildren(controls);
      // The modal is injected via plain innerHTML, so htmx never processed its
      // elements (e.g. the share button's hx-post). Initialize them here.
      htmx.process(document.getElementById('item-dialog'));
      markRowRead(el.dataset.itemId);
    })
    .catch(function () { body.innerHTML = '<p class="error">could not load item</p>'; });
  document.getElementById('item-dialog').showModal();
  return false;
}
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
      // so initialize it.
      htmx.process(d);
    })
    .catch(function () { d.innerHTML = '<p class="error">could not load lists</p>'; });
}
document.addEventListener('click', function (e) {
  if (e.target.closest && e.target.closest('.item-menu')) return;
  closeItemMenus();
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

// Arrow keys move through the item list while the modal is open.
document.addEventListener('keydown', function (e) {
  if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
  var dialog = document.getElementById('item-dialog');
  if (!dialog || !dialog.open || !currentItemId) return;
  if (e.target.closest && e.target.closest('input, textarea, select')) return;
  var current = document.getElementById('item-' + currentItemId);
  if (!current) return;
  var list = current.closest('ul.items') || current.parentElement;
  var items = Array.prototype.slice.call(list.querySelectorAll('li[id^="item-"]'));
  var idx = items.indexOf(current);
  var next = e.key === 'ArrowRight' ? items[idx + 1] : items[idx - 1];
  if (!next) return;
  var link = next.querySelector('[data-item-id]');
  if (!link) return;
  e.preventDefault();
  openItem(link);
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
    case 'ArrowDown':
      move(1);
      break;
    case 'k':
    case 'ArrowUp':
      move(-1);
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
document.body.addEventListener('htmx:afterSwap', function () {
  applyDisplayMode();
  applyAuthorSort();
  if (activeItemId) {
    var r = document.getElementById(activeItemId);
    if (r) r.classList.add('active-row');
  }
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
