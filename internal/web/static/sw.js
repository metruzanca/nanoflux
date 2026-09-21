// nanoflux service worker. Minimal by design: it exists so the browser treats
// the app as installable (Chrome requires a registered worker with a fetch
// handler), but it does NOT cache anything — the reader is authenticated and
// live, so content always comes from the network. The fetch handler is a
// no-op pass-through; POSTs (auth, htmx mutations) are never touched.
self.addEventListener('install', function () {
  self.skipWaiting();
});

self.addEventListener('activate', function (event) {
  event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', function () {
  // Intentionally empty: keep installability without offline behavior.
});