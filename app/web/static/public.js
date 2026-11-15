// giga-simple-lists : page d'un lien public. On y coche et décoche les éléments d'une
// liste, sans compte, en temps réel (SSE) ; sans réseau, les coches attendent son retour.
'use strict';
(() => {
  // Hauteur réelle de la fenêtre (--app-height, app.css) : dans l'appli installée sur
  // Android, Chrome garde pour 100dvh et 100 % la hauteur du lancement, plus grande que la
  // zone visible (884 px pour 828 sur un Pixel 10) : une liste vide obligeait à défiler.
  const fitHeight = () => document.documentElement.style.setProperty('--app-height', innerHeight + 'px');
  fitHeight();
  addEventListener('resize', fitHeight);
  const TOKEN = document.body.dataset.token;
  const BASE = '/s/' + TOKEN;
  const STATE_KEY = 'listes.public.' + TOKEN; // dernière liste vue, pour l'ouvrir sans réseau
  const QUEUE_KEY = 'listes.public-queue.' + TOKEN; // coches en attente : {id: checked}
  const $ = (id) => document.getElementById(id);
  const el = { list: $('list'), title: $('title'), note: $('note'), items: $('items'), gone: $('gone'), offline: $('offline'), toast: $('toast') };
  const MESSAGES = JSON.parse($('messages').textContent);
  const t = (key, vars = {}) => (MESSAGES[key] || key).replace(/\{(\w+)\}/g, (_, k) => vars[k]);

  const store = {
    get(k) { try { return JSON.parse(localStorage.getItem(k)); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch { /* stockage indisponible ou plein */ } },
    remove(k) { try { localStorage.removeItem(k); } catch { /* stockage indisponible */ } },
  };

  let list = store.get(STATE_KEY); // {name, color, items}
  const queue = store.get(QUEUE_KEY) || {};
  let gone = false;

  let toastTimer;
  function toast(msg) {
    el.toast.textContent = msg;
    el.toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.toast.hidden = true; }, 4000);
  }
  const setOffline = (off) => { el.offline.hidden = !off; };

  // ---------------------------------------------------------------------------
  // Rendu : même ordre que l'appli (catégories avec leurs éléments, cochés en bas de
  // chaque groupe). textContent uniquement.

  function displayOrder(items) {
    const groups = [{ head: null, items: [] }];
    [...items].sort((a, b) => a.position - b.position).forEach((it) => {
      if (it.category) groups.push({ head: it, items: [] });
      else groups[groups.length - 1].items.push(it);
    });
    const ordered = [];
    const done = new Set();
    for (const g of groups) {
      if (g.head) {
        ordered.push(g.head);
        if (g.items.length && g.items.every((it) => it.checked)) done.add(g.head.id);
      }
      ordered.push(...g.items.sort((a, b) => a.checked - b.checked));
    }
    return { ordered, done };
  }

  function render() {
    document.body.classList.remove('booting');
    if (!list || gone) return;
    store.set(STATE_KEY, list);
    document.title = list.name;
    el.title.textContent = list.name;
    el.list.className = 'list c-' + list.color;
    const { ordered, done } = displayOrder(list.items);
    let grouped = false;
    el.items.replaceChildren(...ordered.map((it, i) => {
      const li = document.createElement('li');
      li.className = 'item public-item';
      li.dataset.id = it.id;
      if (it.category) grouped = true;
      const next = ordered[i + 1];
      li.classList.toggle('grouped', grouped);
      li.classList.toggle('group-end', grouped && (!next || !!next.category));
      li.classList.toggle('category', !!it.category);
      li.classList.toggle('done', it.category ? done.has(it.id) : it.checked);
      const text = document.createElement('span');
      text.className = 'text';
      text.textContent = it.text;
      if (it.category) {
        li.append(text);
      } else {
        const row = document.createElement('label');
        row.className = 'public-row';
        const box = document.createElement('input');
        box.type = 'checkbox';
        box.checked = it.checked;
        row.append(box, text);
        li.append(row);
      }
      return li;
    }));
  }

  function showGone() {
    gone = true;
    if (es) es.close();
    store.remove(STATE_KEY);
    store.remove(QUEUE_KEY);
    el.items.replaceChildren();
    el.note.hidden = true;
    el.gone.hidden = false;
    setOffline(false);
  }

  // ---------------------------------------------------------------------------
  // Coches : appliquées tout de suite, envoyées dans l'ordre. Sans réseau, elles restent
  // dans la file (sauvegardée) ; seul le dernier état voulu de chaque élément compte.

  function toggle(id, checked) {
    const it = list && list.items.find((x) => x.id === id);
    if (!it || it.category) return;
    it.checked = checked;
    queue[id] = checked;
    store.set(QUEUE_KEY, queue);
    render();
    flush();
  }

  let flushing = false;
  let retryTimer = null;
  let skipped = false; // événement d'un élément ignoré parce qu'il était dans la file
  async function flush() {
    if (flushing || gone) return;
    flushing = true;
    clearTimeout(retryTimer);
    try {
      // Tant que la file n'est pas vide : une coche faite pendant un envoi (ou un élément
      // recoché entre-temps) part dans la même boucle, pas au prochain geste.
      for (let id; (id = Object.keys(queue)[0]) !== undefined && !gone;) {
        const checked = queue[id];
        const ctl = new AbortController();
        const slow = setTimeout(() => setOffline(true), 4000);
        const abort = setTimeout(() => ctl.abort(), 15000);
        let res;
        try {
          res = await fetch(BASE + '/items/' + id, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ checked }),
            credentials: 'omit',
            signal: ctl.signal,
          });
        } catch {
          return retryLater(true);
        } finally {
          clearTimeout(slow);
          clearTimeout(abort);
        }
        if (res.status === 429) return retryLater(false); // le réseau marche : pas « hors ligne »
        if ([502, 503, 504].includes(res.status)) return retryLater(true);
        setOffline(false);
        if (queue[id] === checked) delete queue[id];
        store.set(QUEUE_KEY, queue);
        if (res.status === 404) { resync(); continue; } // lien révoqué, ou élément supprimé
        if (!res.ok) {
          const err = await res.json().catch(() => ({}));
          toast(err.error || t('refused'));
        }
      }
      // Des coches d'autres visiteurs ont pu arriver pendant l'envoi : état du serveur.
      if (skipped && !Object.keys(queue).length) { skipped = false; resync(); }
    } finally {
      flushing = false;
    }
  }

  function retryLater(offline) {
    if (offline) setOffline(true);
    clearTimeout(retryTimer);
    retryTimer = setTimeout(flush, 5000);
  }

  // État du serveur, avec par-dessus les coches encore en attente. Vrai si le serveur a répondu.
  async function resync() {
    let res;
    try {
      res = await fetch(BASE + '/state', { credentials: 'omit' });
    } catch {
      setOffline(true);
      return false;
    }
    if (res.status === 404) { showGone(); return true; }
    if (!res.ok) return res.status === 429;
    list = await res.json();
    list.items = list.items || [];
    list.items.forEach((it) => { if (it.id in queue) it.checked = queue[it.id]; });
    render();
    return true;
  }

  // ---------------------------------------------------------------------------
  // Temps réel

  let es = null;
  let retryDelay = 3000;
  let lastEvent = Date.now();
  const on = (name, fn) => es.addEventListener(name, (e) => { lastEvent = Date.now(); fn(e.data ? JSON.parse(e.data) : null); });
  function connect() {
    if (gone) return;
    if (es) es.close();
    lastEvent = Date.now();
    es = new EventSource(BASE + '/events');
    es.onopen = () => { lastEvent = Date.now(); retryDelay = 3000; setOffline(false); flush(); resync(); };
    es.onerror = () => {
      if (es.readyState !== EventSource.CLOSED) { setOffline(true); return; }
      // Flux refusé (trop de connexions, 429) ou coupé : si le serveur répond, ce n'est pas
      // « hors ligne » ; on réessaie de plus en plus tard (30 s au plus).
      setTimeout(async () => {
        if (await resync()) setOffline(false);
        retryDelay = Math.min(retryDelay * 2, 30000);
        connect();
      }, retryDelay);
    };
    on('ping', () => {});
    on('item', (it) => {
      if (!list) return;
      if (it.id in queue) { skipped = true; return; }
      const i = list.items.findIndex((x) => x.id === it.id);
      if (i >= 0) list.items[i] = it; else list.items.push(it);
      render();
    });
    on('item-removed', ({ id }) => { if (list) { list.items = list.items.filter((x) => x.id !== id); render(); } });
    on('items-removed', ({ ids }) => { if (list) { list.items = list.items.filter((x) => !ids.includes(x.id)); render(); } });
    on('items-moved', ({ items }) => {
      if (!list) return;
      const pos = new Map(items.map((x) => [x.id, x.position]));
      list.items.forEach((it) => { if (pos.has(it.id)) it.position = pos.get(it.id); });
      render();
    });
    on('list', (l) => { if (list) { list.name = l.name; list.color = l.color; render(); } });
    on('list-removed', showGone);
  }

  el.items.addEventListener('change', (e) => {
    if (e.target.matches('input[type=checkbox]')) toggle(e.target.closest('.item').dataset.id, e.target.checked);
  });

  setInterval(() => { if (!gone && Date.now() - lastEvent > 45000) { setOffline(true); connect(); } }, 5000);
  window.addEventListener('online', flush);
  window.addEventListener('offline', () => setOffline(true));
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return;
    flush();
    if (!es || es.readyState === EventSource.CLOSED) connect();
  });

  if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});

  // Ouverture : la copie locale tout de suite, puis l'état du serveur.
  if (list) {
    list.items.forEach((it) => { if (it.id in queue) it.checked = queue[it.id]; });
    render();
  }
  resync().finally(() => document.body.classList.remove('booting'));
  flush();
  connect();
})();
