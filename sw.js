const CACHE_NAME = 'vin-decoder-v8';
const ASSETS = [
  '/',
  '/index.html',
  '/dtc.html',
  '/manifest.json',
  '/icon-192x192.png',
  '/icon-512x512.png',
  '/icon-180x192.png',
  '/scanner.css',
  '/robots.txt',
  '/sitemap.xml'
];

// Install: cache core assets
self.addEventListener('install', e => {
  e.waitUntil(
    caches.open(CACHE_NAME).then(cache => cache.addAll(ASSETS))
  );
  self.skipWaiting();
});

// Activate: clean old caches
self.addEventListener('activate', e => {
  e.waitUntil(
    caches.keys().then(keys =>
      Promise.all(keys.filter(k => k !== CACHE_NAME).map(k => caches.delete(k)))
    )
  );
  self.clients.claim();
});

// Fetch: network-first, fall back to cache
self.addEventListener('fetch', e => {
  const url = new URL(e.request.url);

  // API calls: always go to network
  if (url.hostname.includes('nhtsa.gov') || url.hostname.includes('googleapis.com')) {
    return;
  }

  // CDN libraries (versioned, immutable): cache-first
  if (url.hostname === 'cdn.jsdelivr.net' || url.hostname === 'webrtc.github.io') {
    e.respondWith(
      caches.match(e.request).then(cached => {
        if (cached) return cached;
        return fetch(e.request).then(response => {
          if (response.ok) {
            const clone = response.clone();
            caches.open(CACHE_NAME).then(cache => cache.put(e.request, clone));
          }
          return response;
        });
      })
    );
    return;
  }

  // scanner.js: cache-first (small, rarely changes)
  if (url.pathname.endsWith('scanner.js')) {
    e.respondWith(
      caches.match(e.request).then(cached => {
        if (cached) return cached;
        return fetch(e.request).then(response => {
          if (response.ok) {
            const clone = response.clone();
            caches.open(CACHE_NAME).then(cache => cache.put(e.request, clone));
          }
          return response;
        });
      })
    );
    return;
  }

  // Manifest: always network-first, bypass cache
  if (url.pathname === '/manifest.json' || url.pathname.endsWith('manifest.json')) {
    e.respondWith(fetch(e.request).catch(() => caches.match(e.request)));
    return;
  }

  // Everything else: network-first, cache fallback
  e.respondWith(
    fetch(e.request).then(response => {
      if (response.ok) {
        const clone = response.clone();
        caches.open(CACHE_NAME).then(cache => cache.put(e.request, clone));
      }
      return response;
    }).catch(() => caches.match(e.request).then(cached => cached || caches.match('/')))
  );
});

// Listen for messages from the main thread
self.addEventListener('message', e => {
  if (e.data === 'skipWaiting') {
    self.skipWaiting();
  }
});
