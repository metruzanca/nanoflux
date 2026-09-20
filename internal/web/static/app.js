// nanoflux frontend helpers. htmx 2.x does not swap 4xx/5xx bodies by default;
// this override lets error fragments render so form errors are visible.
document.addEventListener('htmx:beforeSwap', function (e) {
  if (e.detail.xhr.status >= 400) e.detail.shouldSwap = true;
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