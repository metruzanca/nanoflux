// Resumable media playback.
//
// The server renders a saved position into a player's data-progress-* attributes
// (a native <video>/<audio> element or a YouTube iframe). This module seeks or
// starts at that position and posts the current position back every few seconds
// and when playback pauses or the page is hidden. Progress is stored per user in
// the database, so every login shares it; nothing goes to localStorage.
//
// The item modal injects its body with plain innerHTML, so it calls
// nanofluxInitProgress itself (like hls-video.js) and nanofluxDestroyProgress
// when it replaces or closes the body.
(function () {
  'use strict';

  var SAVE_MS = 5000;
  var players = [];

  function csrf() {
    return window.nanofluxCSRF || '';
  }

  function save(el, position, duration, beacon) {
    var id = el.getAttribute('data-progress-item');
    if (!id) return;
    position = Math.max(0, Math.floor(position || 0));
    duration = Math.max(0, Math.floor(duration || 0));
    var body = new URLSearchParams();
    body.set('position', String(position));
    body.set('duration', String(duration));
    var url = '/items/' + encodeURIComponent(id) + '/progress';
    if (beacon && navigator.sendBeacon) {
      // sendBeacon cannot set headers, so carry the CSRF token in the form.
      body.set('csrf_token', csrf());
      navigator.sendBeacon(url, body);
      return;
    }
    fetch(url, { method: 'POST', body: body, keepalive: true }).catch(function () {});
  }

  function clearProgress(el) {
    var id = el.getAttribute('data-progress-item');
    if (!id) return;
    fetch('/items/' + encodeURIComponent(id) + '/progress', { method: 'DELETE' }).catch(function () {});
  }

  function attrInt(el, name) {
    return parseInt(el.getAttribute(name), 10) || 0;
  }

  function initNative(el) {
    var resume = attrInt(el, 'data-progress-resume');
    var storedDuration = attrInt(el, 'data-progress-duration');
    var lastSave = 0;
    var seeked = false;

    function durationNow() {
      return isFinite(el.duration) && el.duration > 0 ? Math.floor(el.duration) : storedDuration;
    }

    function maybeSeek() {
      if (seeked || resume < 10) return;
      seeked = true;
      if (isFinite(el.duration) && resume < el.duration - 5) {
        try { el.currentTime = resume; } catch (err) { /* seek unsupported */ }
      }
    }

    function flush() {
      save(el, el.currentTime, durationNow(), false);
    }

    el.addEventListener('loadedmetadata', maybeSeek);
    el.addEventListener('timeupdate', function () {
      var now = Date.now();
      if (now - lastSave < SAVE_MS) return;
      lastSave = now;
      flush();
    });
    el.addEventListener('pause', flush);
    el.addEventListener('ended', function () {
      // Saving the full duration makes the server drop the row, so a finished
      // video starts from the beginning next time.
      var total = durationNow();
      save(el, total, total, false);
    });
    el.addEventListener('seeked', function () {
      if (el.currentTime < 5) clearProgress(el);
    });

    return {
      el: el,
      flush: function () { save(el, el.currentTime, durationNow(), true); },
      destroy: function () {},
    };
  }

  function initYouTube(frame) {
    var win = frame.contentWindow;
    if (!win) return null;
    var current = 0;
    var duration = attrInt(frame, 'data-progress-duration');
    var playing = false;
    var pendingSave = false;
    var lastSave = Date.now();
    var id = 1;

    function send(msg) {
      msg.id = id;
      try { win.postMessage(JSON.stringify(msg), '*'); } catch (err) { /* gone */ }
    }
    function listen() {
      send({ event: 'listening', channel: 'widget' });
    }
    function flush() {
      save(frame, current, duration, false);
    }

    frame.addEventListener('load', listen);
    listen();

    function onMessage(e) {
      if (e.source !== win) return;
      var data;
      try { data = JSON.parse(e.data); } catch (err) { return; }
      if (!data || data.event !== 'infoDelivery' || !data.info) return;
      var info = data.info;
      if (typeof info.currentTime === 'number') current = info.currentTime;
      if (typeof info.duration === 'number' && info.duration > 0) duration = info.duration;
      if (typeof info.playerState === 'number') {
        var was = playing;
        playing = info.playerState === 1;
        if (was && !playing) {
          pendingSave = true;
          send({ event: 'command', func: 'getCurrentTime', args: [] });
        }
        if (info.playerState === 0) {
          // Ended: store the full duration so the server clears the row.
          save(frame, duration, duration, false);
        }
      }
      if (pendingSave) {
        pendingSave = false;
        flush();
      }
      if (playing) {
        var now = Date.now();
        if (now - lastSave >= SAVE_MS) {
          lastSave = now;
          flush();
        }
      }
    }
    window.addEventListener('message', onMessage);

    var timer = setInterval(function () {
      if (!document.body.contains(frame)) return;
      if (!playing) return;
      send({ event: 'command', func: 'getCurrentTime', args: [] });
      send({ event: 'command', func: 'getDuration', args: [] });
    }, SAVE_MS);

    return {
      el: frame,
      flush: function () { save(frame, current, duration, true); },
      destroy: function () {
        clearInterval(timer);
        window.removeEventListener('message', onMessage);
      },
    };
  }

  function initOne(el) {
    if (el.dataset.progressReady === '1') return;
    el.dataset.progressReady = '1';
    var player = null;
    if (el.tagName === 'IFRAME') player = initYouTube(el);
    else player = initNative(el);
    if (player) players.push(player);
  }

  function initAll(root) {
    (root || document).querySelectorAll('[data-progress-item]').forEach(initOne);
  }

  function destroyAll(root) {
    players = players.filter(function (p) {
      var gone = !p.el.isConnected || (root && root.contains(p.el));
      if (gone) {
        p.flush();
        p.destroy();
      }
      return !gone;
    });
  }

  // A hidden tab or a navigation is the last chance to save the position.
  document.addEventListener('visibilitychange', function () {
    if (document.visibilityState !== 'hidden') return;
    players.forEach(function (p) { p.flush(); });
  });
  window.addEventListener('pagehide', function () {
    players.forEach(function (p) { p.flush(); });
  });

  window.nanofluxInitProgress = initAll;
  window.nanofluxDestroyProgress = destroyAll;

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { initAll(document); });
  } else {
    initAll(document);
  }
  // The item modal calls nanofluxInitProgress itself; this covers htmx swaps
  // and the server-rendered public share page.
  document.body.addEventListener('htmx:afterSwap', function (e) {
    var t = e.detail && e.detail.target;
    initAll(t && t.isConnected ? t : document);
  });
})();
