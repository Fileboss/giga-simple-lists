// Listes : ouverture sans réseau. Le réseau passe toujours en premier (jamais de
// version périmée quand il répond) ; le cache ne sert que s'il ne répond pas.
// L'API et le flux SSE ne passent pas par ici : app.js garde sa propre copie des listes.
'use strict';

const CACHE = 'listes-v1';
const PAGE = '/'; // la page de l'appli : propre à la session, effacée à la déconnexion
const ASSETS = ['/static/app.js', '/static/app.css', '/static/check.svg', '/static/icon.svg',
  '/static/icon-192.png', '/manifest.webmanifest'];
const TIMEOUT = 4000; // réseau qui ne répond pas (supermarché) : même seuil que le bandeau d'app.js

// Pages ouvertes depuis le cache : leurs fichiers viennent aussi du cache, sans attendre le
// réseau, et restent de la même version que la page.
const offlineClients = new Set();

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(ASSETS)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil((async () => {
    for (const k of await caches.keys()) if (k !== CACHE) await caches.delete(k);
    // La page qui vient d'enregistrer ce worker n'est pas passée par lui : on la met en cache tout de suite.
    try {
      const res = await fetch(PAGE, { credentials: 'same-origin' });
      if (res.ok && !res.redirected) await (await caches.open(CACHE)).put(PAGE, res);
    } catch { /* hors ligne : ce sera fait à la prochaine ouverture */ }
    await self.clients.claim();
  })());
});

const forget = () => caches.open(CACHE).then((c) => c.delete(PAGE));

self.addEventListener('fetch', (e) => {
  const req = e.request;
  const url = new URL(req.url);
  if (url.origin !== location.origin) return;
  if (url.pathname === '/logout') { e.waitUntil(forget()); return; }
  if (req.method !== 'GET') return;
  if (req.mode === 'navigate' && url.pathname === PAGE) {
    e.respondWith(page(e));
  } else if (url.pathname.startsWith('/static/') || url.pathname === '/manifest.webmanifest') {
    e.respondWith(offlineClients.has(e.clientId) ? fromCache(req) : networkFirst(e, req).then((r) => r.response));
  }
});

async function page(e) {
  const { response, fromCache } = await networkFirst(e, e.request, PAGE);
  // Session expirée : le serveur redirige vers /login ; la page en cache n'est plus valable.
  if (response.type === 'opaqueredirect') e.waitUntil(forget());
  if (fromCache) offlineClients.add(e.resultingClientId);
  return response;
}

async function fromCache(req) {
  return (await caches.match(req)) || fetch(req);
}

// Le réseau, mis en cache s'il répond ; sinon (erreur, pas de réponse sous TIMEOUT, serveur
// injoignable derrière le reverse proxy) la copie en cache s'il y en a une, et à défaut
// la réponse du réseau quand elle arrive. Renvoie { response, fromCache }.
function networkFirst(e, req, key = req) {
  const net = fetch(req).then((res) => {
    if (res.ok && res.type === 'basic') {
      const copy = res.clone();
      e.waitUntil(caches.open(CACHE).then((c) => c.put(key, copy)));
    }
    return res;
  });
  e.waitUntil(net.catch(() => {}));
  return new Promise((resolve) => {
    let done = false;
    const answer = (response, fromCache) => { if (!done) { done = true; resolve({ response, fromCache }); } };
    // res : la réponse du réseau à rendre faute de copie (null : on l'attend encore).
    const fallback = async (res) => {
      const hit = await caches.match(key);
      if (hit) answer(hit, true); else if (res) answer(res, false);
    };
    const timer = setTimeout(() => fallback(null), TIMEOUT);
    net.then((res) => {
      clearTimeout(timer);
      if (res.status === 502 || res.status === 503 || res.status === 504) fallback(res);
      else answer(res, false);
    }, () => {
      clearTimeout(timer);
      fallback(Response.error());
    });
  });
}
