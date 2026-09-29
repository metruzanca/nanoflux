// HLS video playback for enclosures with a data-hls attribute (a .m3u8
// manifest). HLS is not playable natively in Chrome/Firefox, so this attaches
// hls.js. Safari plays HLS natively, so it is left to the browser there.
//
// hls.js is loaded lazily, only when a page actually has an HLS video, so the
// ~380 KB build is not fetched for readers who never open one. The manifest is
// fetched at attach time (the video element keeps preload="none"; hls.js loads
// the small manifest and buffers as needed).
(function () {
  'use strict';

  var loading = null;

  function loadHls() {
    if (window.Hls) return Promise.resolve(window.Hls);
    if (loading) return loading;
    loading = new Promise(function (resolve, reject) {
      var s = document.createElement('script');
      s.src = '/static/hls.light.min.js';
      s.onload = function () { resolve(window.Hls); };
      s.onerror = function () { reject(new Error('hls.js failed to load')); };
      document.head.appendChild(s);
    });
    return loading;
  }

  function initOne(video) {
    if (video.dataset.hlsReady === '1') return;
    var src = video.getAttribute('data-hls');
    if (!src) return;
    video.dataset.hlsReady = '1';

    // Prefer hls.js when it is supported (MSE): Chrome reports "maybe" for
    // canPlayType('application/vnd.apple.mpegurl') yet cannot actually play HLS,
    // so native support cannot be the first check. Safari plays natively, and
    // hls.js also works there via MSE, so hls.js first is correct everywhere.
    loadHls().then(function (Hls) {
      if (Hls && Hls.isSupported()) {
        var hls = new Hls({ enableWorker: false });
        hls.loadSource(src);
        hls.attachMedia(video);
        video._hls = hls;
        return;
      }
      // No MSE (e.g. iOS Safari): let the browser play the manifest natively.
      video.src = src;
    }).catch(function () {
      // hls.js failed to load; try native playback as a last resort.
      video.src = src;
    });
  }

  function initAll(root) {
    (root || document).querySelectorAll('video[data-hls]').forEach(initOne);
  }

  // destroy stops network activity for videos being removed (the item modal
  // replaces its body on each open, and closes entirely).
  function destroyAll(root) {
    (root || document).querySelectorAll('video[data-hls]').forEach(function (video) {
      if (video._hls) {
        try { video._hls.destroy(); } catch (err) { /* already detached */ }
        video._hls = null;
      }
    });
  }

  window.nanofluxInitHLS = initAll;
  window.nanofluxDestroyHLS = destroyAll;

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { initAll(document); });
  } else {
    initAll(document);
  }
  // The item modal (fetch + innerHTML) calls nanofluxInitHLS itself; this covers
  // htmx swaps and the server-rendered public share page.
  document.body.addEventListener('htmx:afterSwap', function (e) {
    var t = e.detail && e.detail.target;
    initAll(t && t.isConnected ? t : document);
  });
})();
