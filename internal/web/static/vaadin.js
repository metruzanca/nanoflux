// Vaadin combo-box submission bridge for nanoflux.
//
// The vendored components render inline `items`/`selected-items` attributes
// themselves (Lit JSON-parses Array-typed attributes), so no option hydration
// happens here. This script exists only because the components are not
// form-associated and their internal input holds display text: htmx serializes a
// form with FormData, which would submit the label (single-select) or nothing
// (multi-select). Each wrapper therefore pairs the component with hidden native
// <input> mirrors, and this script keeps them in sync:
//
//   - single select: one mirror carries the value and any hx-* attributes (so
//     hx-trigger="change" still drives the dynamic selects).
//   - multi select: one mirror per selected value, all sharing a name, so the
//     field submits as repeated params like the checkbox group it replaces.
//
//   - chips: no mirrors; it is display-only, and a removed chip POSTs to the
//     wrapper's data-remove-url.
(function () {
  'use strict';

  var SINGLE = 'vaadin-combo-box';
  var MULTI = 'vaadin-multi-select-combo-box';

  function valueOfItem(item) {
    if (item && typeof item === 'object') return String(item.value);
    return String(item);
  }

  function fireChange(el) {
    el.dispatchEvent(new Event('change', { bubbles: true }));
  }

  // initSingle mirrors the component value into the hidden input and fires
  // "change" so htmx runs the input's hx-trigger.
  function initSingle(wrap, combo) {
    var mirror = wrap.querySelector('input[data-vaadin-target]');
    if (!mirror) return;
    var apply = function () {
      var next = combo.value == null ? '' : String(combo.value);
      if (mirror.value === next) return;
      mirror.value = next;
      fireChange(mirror);
    };
    apply();
    combo.addEventListener('value-changed', apply);
  }

  // initMulti rebuilds the hidden inputs to match the component's selected
  // items. The first input is fired with "change" so any hx-trigger on the field
  // still sees an update.
  function initMulti(wrap, combo) {
    var host = wrap.querySelector('[data-vaadin-mirrors]');
    if (!host) return;
    var name = host.dataset.vaadinName || '';
    var apply = function () {
      var next = (combo.selectedItems || []).map(valueOfItem);
      var prev = Array.prototype.map.call(host.querySelectorAll('input'), function (i) { return i.value; });
      if (prev.length === next.length && prev.every(function (v, i) { return v === next[i]; })) return;
      host.textContent = '';
      next.forEach(function (v) {
        var input = document.createElement('input');
        input.type = 'hidden';
        input.name = name;
        input.value = v;
        host.appendChild(input);
      });
      if (host.firstChild) fireChange(host.firstChild);
    };
    apply();
    combo.addEventListener('selected-items-changed', apply);
  }

  // initChips wires a display-only multi-select whose chips remove items from a
  // server endpoint. Each removed value POSTs to data-remove-url (with the id
  // appended); on failure the selection is restored so the UI matches the
  // server.
  function initChips(wrap, combo) {
    var urlTemplate = wrap.dataset.removeUrl || '';
    if (!urlTemplate) return;
    var known = (combo.selectedItems || []).map(valueOfItem);
    combo.addEventListener('selected-items-changed', function () {
      var now = (combo.selectedItems || []).map(valueOfItem);
      var removed = known.filter(function (v) { return now.indexOf(v) === -1; });
      if (!removed.length) { known = now; return; }
      var previousValues = known.slice();
      known = now;
      removed.forEach(function (id) {
        var url = urlTemplate + encodeURIComponent(id);
        fetch(url, { method: 'POST', headers: { 'HX-Request': 'true' } })
          .then(function (r) { if (!r.ok) throw new Error('remove failed'); })
          .catch(function () {
            // Restore by value: keep the items, reselect the previous set.
            var all = combo.items || [];
            var byValue = {};
            all.forEach(function (it) { byValue[valueOfItem(it)] = it; });
            combo.selectedItems = previousValues.map(function (v) { return byValue[v]; }).filter(Boolean);
            known = previousValues;
          });
      });
    });
  }

  function initOne(wrap) {
    if (wrap.dataset.vaadinReady === '1') return;
    var kind = wrap.dataset.vaadin;
    if (kind === 'single') {
      var combo = wrap.querySelector(SINGLE);
      if (!combo) return;
      initSingle(wrap, combo);
    } else if (kind === 'multi') {
      var mcombo = wrap.querySelector(MULTI);
      if (!mcombo) return;
      initMulti(wrap, mcombo);
    } else if (kind === 'chips') {
      var ccombo = wrap.querySelector(MULTI);
      if (!ccombo) return;
      initChips(wrap, ccombo);
    } else {
      return;
    }
    wrap.dataset.vaadinReady = '1';
  }

  function initAll(root) {
    (root || document).querySelectorAll('[data-vaadin]').forEach(initOne);
  }

  // The custom elements upgrade asynchronously (the bundle is deferred), so wait
  // for them before reading properties and binding listeners.
  function ready() {
    if (!window.customElements) return Promise.resolve();
    return Promise.all([
      customElements.whenDefined(SINGLE),
      customElements.whenDefined(MULTI),
    ]).catch(function () {});
  }

  function boot(root) {
    ready().then(function () { initAll(root || document); });
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { boot(document); });
  } else {
    boot(document);
  }
  // Re-run after htmx swaps in new wrappers (dialogs, settings cards).
  document.body.addEventListener('htmx:afterSwap', function (e) {
    boot(e.detail.target || document);
  });
  // Some fragments are injected with fetch + innerHTML (the add-to-list dialog),
  // which fires no htmx swap event, so expose an explicit re-init hook callers
  // use after injecting markup and running htmx.process.
  window.nanofluxReinitVaadin = function (root) { boot(root || document); };
})();
