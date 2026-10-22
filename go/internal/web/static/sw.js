/* sdrstream service worker.
 *
 * The app is a live monitor: API responses and audio must always be fresh,
 * so only the static shell (page, CSS, JS, icons) is cached. Everything
 * under /api/, /audio/, /gallery/ and /login goes straight to the network.
 * Bump CACHE when the shell assets change so old caches are dropped.
 */
const CACHE = 'sdrstream-shell-v1';
const SHELL = [
  '/',
  '/static/app.css',
  '/static/app.js',
  '/static/icons/icon-192.png',
  '/static/icons/icon-512.png',
  '/static/icons/apple-touch-icon.png',
  '/manifest.webmanifest'
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE)
      .then((cache) => cache.addAll(SHELL))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

function isLiveData(url) {
  return url.pathname.startsWith('/api/') ||
    url.pathname.startsWith('/audio/') ||
    url.pathname.startsWith('/gallery/') ||
    url.pathname === '/login' ||
    url.pathname === '/logout';
}

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  if (event.request.method !== 'GET' || url.origin !== self.location.origin) return;
  if (isLiveData(url)) return; // network only
  if (event.request.mode === 'navigate') {
    // Network first for the page itself, cached shell as an offline fallback.
    event.respondWith(
      fetch(event.request)
        .then((resp) => {
          const copy = resp.clone();
          caches.open(CACHE).then((cache) => cache.put('/', copy));
          return resp;
        })
        .catch(() => caches.match('/'))
    );
    return;
  }
  // Static assets: cache first, then network (and cache the result).
  event.respondWith(
    caches.match(event.request).then((hit) => hit || fetch(event.request).then((resp) => {
      if (resp.ok && url.pathname.startsWith('/static/')) {
        const copy = resp.clone();
        caches.open(CACHE).then((cache) => cache.put(event.request, copy));
      }
      return resp;
    }))
  );
});
