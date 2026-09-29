// Umami analytics bridge, loaded only when analytics is enabled (see
// views_layout.templ). It is named by the tracker script's data-before-send
// attribute, so the tracker calls window.nfBeforeSend before every send.
//
// Pageviews are rewritten to a route pattern (e.g. /authors/15 -> /authors/:id)
// so analytics groups by page shape rather than by entity id or share token.
// The item modal's #item-<id> hash is dropped and the search query (?q=) is
// removed, but campaign params (utm_*) and list filters are kept. A pageview
// equal to the previous one is cancelled so opening/closing the modal does not
// re-count the list underneath it. Custom events and identify/performance
// sends pass through untouched.
(function () {
  var lastPage = null;
  var tokenSegments = { shared: true, l: true, f: true, b: true };

  function normalize(raw) {
    if (!raw) return raw;
    var u;
    try {
      u = new URL(raw, location.origin);
    } catch (e) {
      return raw;
    }
    var parts = u.pathname.split('/');
    for (var i = 0; i < parts.length; i++) {
      var seg = parts[i];
      if (!seg) continue;
      if (/^\d+$/.test(seg)) {
        parts[i] = ':id';
      } else if (i > 0 && tokenSegments[parts[i - 1]]) {
        parts[i] = ':token';
      }
    }
    u.pathname = parts.join('/');
    u.searchParams.delete('q');
    return u.pathname + (u.search || '');
  }

  window.nfBeforeSend = function (type, payload) {
    if (!payload) return payload;
    if (type !== 'event' || payload.name) return payload;
    var url = normalize(payload.url);
    if (url === lastPage) return false;
    lastPage = url;
    payload.url = url;
    return payload;
  };
})();
