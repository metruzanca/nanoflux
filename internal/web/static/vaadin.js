// Vaadin combo-box integration for nanoflux.
//
// The vendored components are client-side custom elements. htmx serializes a
// form with FormData, which only sees native/form-associated controls, so a
// <vaadin-combo-box> alone would submit nothing. Each wrapper therefore pairs
// the component with hidden native <input> mirrors:
//
//   - single select: one hidden input carries the value and any hx-* attributes
//     (so hx-trigger="change" still drives the dynamic selects).
//   - multi select: one hidden input per selected value, all sharing a name, so
//     the field submits as repeated params exactly like the checkbox group it
//     replaces. The server renders the initial inputs; this script rebuilds them
//     on change.
//
// Items are server-rendered as a JSON payload in a sibling
// <script type="application/json" data-vaadin-items>, because the components
// take their options as a JS array property, not as child elements.
(function () {
  'use strict';

  var SINGLE = 'vaadin-combo-box';
  var MULTI = 'vaadin-multi-select-combo-box';

  function parsePayload(wrap) {
    var el = wrap.querySelector('script[type="application/json"]');
    if (!el) return null;
    try {
      return JSON.parse(el.textContent);
    } catch (err) {
      return null;
    }
  }

  function itemsOf(payload) {
    if (!payload || !Array.isArray(payload.items)) return [];
    return payload.items.map(function (it) {
      if (typeof it === 'string') return { value: it, label: it };
      return {
        value: String(it.value),
        label: it.label == null ? String(it.value) : String(it.label),
      };
    });
  }

  function valuesOf(payload) {
    if (!payload || payload.value == null) return [];
    var v = payload.value;
    if (!Array.isArray(v)) v = [v];
    return v.map(String);
  }

  function fireChange(el) {
    el.dispatchEvent(new Event('change', { bubbles: true }));
  }

  // initSingle mirrors one value into the hidden input and fires "change" so
  // htmx runs the input's hx-trigger.
  function initSingle(wrap, combo, payload) {
    combo.items = itemsOf(payload);
    var initial = valuesOf(payload);
    if (initial.length) combo.value = initial[0];

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

  // initMulti rebuilds the hidden inputs named by data-vaadin-name to match the
  // component's selected items. The first input is fired with "change" so any
  // hx-trigger on the field still sees an update.
  function initMulti(wrap, combo, payload) {
    combo.items = itemsOf(payload);
    var host = wrap.querySelector('[data-vaadin-mirrors]');
    if (!host) return;
    var name = wrap.dataset.vaadinName || host.dataset.vaadinName || '';
    var apply = function () {
      var next = (combo.selectedItems || []).map(String);
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
    combo.selectedItems = valuesOf(payload);
    apply();
    combo.addEventListener('selected-items-changed', apply);
  }

  // initChips wires a display-only multi-select whose chips remove items from a
  // server endpoint. Each removed value POSTs to data-remove-url (with {id}
  // substituted); on failure the selection is restored so the UI matches the
  // server.
  function initChips(wrap, combo, payload) {
    combo.items = itemsOf(payload);
    var known = valuesOf(payload);
    combo.selectedItems = known;
    var urlTemplate = wrap.dataset.removeUrl || '';
    if (!urlTemplate) return;
    combo.addEventListener('selected-items-changed', function () {
      var now = (combo.selectedItems || []).map(String);
      var removed = known.filter(function (v) { return now.indexOf(v) === -1; });
      if (!removed.length) { known = now; return; }
      var previous = known.slice();
      known = now;
      removed.forEach(function (id) {
        var url = urlTemplate + encodeURIComponent(id);
        fetch(url, { method: 'POST', headers: { 'HX-Request': 'true' } })
          .then(function (r) { if (!r.ok) throw new Error('remove failed'); })
          .catch(function () { combo.selectedItems = previous; known = previous; });
      });
    });
  }

  function initOne(wrap) {
    if (wrap.dataset.vaadinReady === '1') return;
    var payload = parsePayload(wrap);
    if (!payload) return;
    var kind = wrap.dataset.vaadin;
    if (kind === 'single') {
      var combo = wrap.querySelector(SINGLE);
      if (!combo) return;
      initSingle(wrap, combo, payload);
    } else if (kind === 'multi') {
      var mcombo = wrap.querySelector(MULTI);
      if (!mcombo) return;
      initMulti(wrap, mcombo, payload);
    } else if (kind === 'chips') {
      var ccombo = wrap.querySelector(MULTI);
      if (!ccombo) return;
      initChips(wrap, ccombo, payload);
    } else {
      return;
    }
    wrap.dataset.vaadinReady = '1';
  }

  function initAll(root) {
    (root || document).querySelectorAll('[data-vaadin]').forEach(initOne);
  }

  // The custom elements upgrade asynchronously (the bundle is deferred), so wait
  // for them before setting properties on an inert node.
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
})();
