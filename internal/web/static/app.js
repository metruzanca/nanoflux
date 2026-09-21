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

// Item modal.
var currentItemId = null;
function openItem(el) {
  currentItemId = el.dataset.itemId;
  document.getElementById('item-dialog-live').href = el.dataset.itemLink;
  var body = document.getElementById('item-dialog-body');
  body.innerHTML = '<p class="muted">loading…</p>';
  fetch('/items/' + el.dataset.itemId + '/view')
    .then(function (r) { return r.text(); })
    .then(function (html) {
      body.innerHTML = html;
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
// toggles swap it via outerHTML).
document.body.addEventListener('htmx:afterSwap', function () {
  if (activeItemId) {
    var r = document.getElementById(activeItemId);
    if (r) r.classList.add('active-row');
  }
});

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