// Home-screen pinned-collection grid: rendering and drag-to-reorder.
//
// The grid renders each row's content imperatively (via a column renderer), so
// this is the one place with glue. The markup itself stays server-rendered: the
// card emits one <template id="settings-home-row-{id}"> per pinned collection,
// and the renderer clones the matching template into the cell (then htmx.process
// so the row's hx-* attributes work). Reordering posts the new order to the
// card's data-reorder-url and swaps the card back.
(function () {
  'use strict';

  function rowTemplate(item) {
    return document.getElementById('settings-home-row-' + item.id);
  }

  // renderRow clones the item's template into a grid cell.
  function renderRow(root, column, model) {
    root.textContent = '';
    const tpl = rowTemplate(model.item);
    if (!tpl) return;
    const frag = document.importNode(tpl.content, true);
    root.appendChild(frag);
    if (window.htmx) htmx.process(root);
  }

  // initGrid wires one grid: its column renderer and its reorder handler.
  function initGrid(grid) {
    if (grid.dataset.homeGridReady === '1') return;
    const column = grid.querySelector('vaadin-grid-column');
    if (!column) return;
    column.renderer = renderRow;

    const reorderURL = grid.dataset.reorderUrl;
    if (reorderURL) {
      grid.addEventListener('grid-drop', function (e) {
        const dragged = (e.detail.draggedItems || [])[0];
        const target = e.detail.dropTargetItem;
        if (!dragged || !target || String(dragged.id) === String(target.id)) return;
        const ids = (grid.items || []).map(function (it) { return String(it.id); });
        const order = ids.filter(function (id) { return id !== String(dragged.id); });
        const at = order.indexOf(String(target.id));
        // "between" drops report above/below; insert accordingly.
        const insertAt = e.detail.dropLocation === 'below' ? at + 1 : at;
        order.splice(insertAt, 0, String(dragged.id));
        if (!window.htmx) return;
        // Pass a plain object: htmx's value encoder iterates own keys and
        // repeats array entries, so `order` arrives as repeated params.
        htmx.ajax('POST', reorderURL, {
          target: '#settings-home-card',
          swap: 'outerHTML',
          values: { action: 'reorder', order: order },
        });
      });
    }
    grid.dataset.homeGridReady = '1';
  }

  function initAll(root) {
    (root || document).querySelectorAll('vaadin-grid[data-reorder-url], #settings-home-grid').forEach(initGrid);
  }

  function ready() {
    if (!window.customElements) return Promise.resolve();
    return customElements.whenDefined('vaadin-grid').catch(function () {});
  }

  function boot(root) {
    ready().then(function () { initAll(root || document); });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { boot(document); });
  } else {
    boot(document);
  }
  // Re-run after htmx swaps the card in. The swap target may be detached (an
  // outerHTML swap replaces it), so fall back to scanning the whole document.
  document.body.addEventListener('htmx:afterSwap', function (e) {
    var t = e.detail && e.detail.target;
    boot(t && t.isConnected ? t : document);
  });
  window.nanofluxReinitHomeGrid = function (root) { boot(root || document); };
})();
