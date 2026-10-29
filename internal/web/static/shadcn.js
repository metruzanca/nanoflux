// shadcn-templ glue for nanoflux.
//
// The component bundle brings its own MutationObserver, so swapped and cloned
// components initialize themselves. This file covers the two gaps between that
// runtime and the rest of the app:
//
//   1. shadcn portals popups to <body>. A native <dialog> lives in the top
//      layer, so a body-portaled combobox inside one would render behind it.
//      Redirect the portal into the nearest <dialog> instead.
//   2. The slider updates its range input while dragging but only emits its
//      own slider-change event. Flush a native "change" on pointerup/keyup so
//      a surrounding form's hx-trigger="change" fires once, after the drag.
(function () {
  'use strict';

  var portal = window.templ && window.templ.portal;
  if (portal && typeof portal.render === 'function') {
    var render = portal.render;
    portal.render = function (node) {
      var parent = node && node.parentElement;
      var dialog = parent && parent.closest ? parent.closest('dialog') : null;
      if (dialog) {
        dialog.appendChild(node);
        return;
      }
      return render.call(portal, node);
    };
  }

  var pendingSliders = new Set();

  function flushSlider(root) {
    pendingSliders.delete(root);
    var input = root.querySelector('input[type="range"]');
    if (input) input.dispatchEvent(new Event('change', { bubbles: true }));
  }

  function flushAllSliders() {
    pendingSliders.forEach(flushSlider);
  }

  document.addEventListener('slider-change', function (e) {
    var slider = e.target && e.target.closest ? e.target.closest('[data-slot="slider"]') : null;
    if (slider) pendingSliders.add(slider);
  });

  document.addEventListener('pointerup', flushAllSliders);
  document.addEventListener('pointercancel', flushAllSliders);
  document.addEventListener('keyup', function (e) {
    var slider = e.target && e.target.closest ? e.target.closest('[data-slot="slider"]') : null;
    if (slider) pendingSliders.add(slider);
    flushAllSliders();
  });
})();
