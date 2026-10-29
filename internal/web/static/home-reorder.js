// Home-screen pinned-collection reorder.
//
// Rows are plain server-rendered markup with data-home-row and data-id. A
// single delegated HTML5 drag handler computes the new order and posts it to
// the card's data-reorder-url; the server swaps the card back. No re-init is
// needed after a swap because every listener is delegated.
(function () {
  'use strict';

  var dragged = null;

  function rowOf(el) {
    return el && el.closest ? el.closest('[data-home-row]') : null;
  }

  function gridOf(row) {
    return row && row.closest ? row.closest('#settings-home-grid') : null;
  }

  function cleanup() {
    if (dragged) dragged.classList.remove('dragging');
    dragged = null;
  }

  document.addEventListener('dragstart', function (e) {
    // Let links and controls keep their native drag or click behavior; the row
    // background is the reorder target.
    if (e.target.closest && e.target.closest('a,button,input,select,textarea')) return;
    var row = rowOf(e.target);
    if (!row || !gridOf(row)) return;
    dragged = row;
    row.classList.add('dragging');
    if (e.dataTransfer) {
      e.dataTransfer.effectAllowed = 'move';
      try { e.dataTransfer.setData('text/plain', row.dataset.id || ''); } catch (err) {}
    }
  });

  document.addEventListener('dragover', function (e) {
    if (!dragged) return;
    var grid = gridOf(dragged);
    if (!grid || !grid.contains(e.target)) return;
    e.preventDefault();
    if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
  });

  document.addEventListener('drop', function (e) {
    if (!dragged) return;
    var grid = gridOf(dragged);
    if (!grid) {
      cleanup();
      return;
    }
    var target = rowOf(e.target);
    if (!target || target === dragged || !grid.contains(target)) {
      cleanup();
      return;
    }
    e.preventDefault();
    var ids = Array.prototype.map.call(grid.querySelectorAll('[data-home-row]'), function (row) {
      return row.dataset.id;
    });
    var moving = dragged.dataset.id;
    var order = ids.filter(function (id) { return id !== moving; });
    var at = order.indexOf(target.dataset.id);
    var rect = target.getBoundingClientRect();
    if (e.clientY > rect.top + rect.height / 2) at += 1;
    order.splice(at, 0, moving);

    var url = grid.dataset.reorderUrl;
    cleanup();
    if (!url || !window.htmx) return;
    // htmx encodes a plain object's array values as repeated params.
    htmx.ajax('POST', url, {
      target: '#settings-home-card',
      swap: 'outerHTML',
      values: { action: 'reorder', order: order },
    });
  });

  document.addEventListener('dragend', cleanup);
})();
