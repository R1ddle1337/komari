// Browser HTTP caching handles fingerprinted assets. Do not intercept requests
// or precache whole releases: the public theme and admin UI share this scope.
self.addEventListener("install", (event) => {
  event.waitUntil(self.skipWaiting());
});
self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    const names = await caches.keys();
    await Promise.all(names.filter((name) => name.startsWith("workbox-precache-")).map((name) => caches.delete(name)));
    await self.clients.claim();
  })());
});
