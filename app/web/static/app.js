// Listes : rendu, mises à jour optimistes, file d'attente hors ligne, temps réel (SSE).
'use strict';
(() => {
  const ME = document.body.dataset.me;
  const CSRF = document.querySelector('meta[name="csrf-token"]').content;
  const QUEUE_KEY = 'listes.queue.' + ME;
  const LAST_KEY = 'listes.last.' + ME;
  const STATE_KEY = 'listes.state.' + ME; // dernières listes affichées, pour ouvrir l'appli sans réseau
  const $ = (id) => document.getElementById(id);
  const el = {
    tabs: $('tabs'), tabsPrev: $('tabs-prev'), tabsNext: $('tabs-next'), newList: $('new-list'), offline: $('offline'), toast: $('toast'),
    list: $('list'), title: $('title'), share: $('share'), ownerNote: $('owner-note'),
    add: $('add'), newItem: $('new-item'), addBtn: $('add-btn'), items: $('items'), checkedTools: $('checked-tools'), uncheck: $('uncheck-all'), clear: $('clear-checked'),
    ownerTools: $('owner-tools'), del: $('delete'), confirm: $('confirm-delete'),
    confirmText: $('confirm-text'), delYes: $('delete-yes'), delNo: $('delete-no'), empty: $('empty'),
    listHead: $('list-head'), switcher: $('switcher'), switcherName: $('switcher-name'),
    sheet: $('sheet'), sheetLists: $('sheet-lists'), sheetNew: $('sheet-new'), sheetRename: $('sheet-rename'),
    sheetUp: $('sheet-up'), sheetDown: $('sheet-down'), sheetGrip: $('sheet-grip'),
    undo: $('undo'), undoText: $('undo-text'), undoBtn: $('undo-btn'),
  };
  // Téléphone : sélecteur en panneau à la place des onglets (même seuil que le CSS).
  const mobile = matchMedia('(max-width: 699px)');
  const DEFAULT_LIST_NAME = document.body.dataset.defaultListName;
  const COLORS = [...document.querySelectorAll('input[name="color"]')].map((i) => i.value);
  // Messages dans la langue de l'instance (LISTES_LANG, i18n.go) ; {nom} est remplacé par vars.nom.
  const MESSAGES = JSON.parse($('messages').textContent);
  const t = (key, vars = {}) => (MESSAGES[key] || key).replace(/\{(\w+)\}/g, (_, k) => vars[k]);

  let lists = [];          // [{id, owner_id, owner_name, name, color, shared, position, items: [...]}]
  let current = null;      // id de la liste affichée
  let loaded = false;

  // ---------------------------------------------------------------------------
  // Utilitaires

  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* stockage indisponible ou plein */ } },
    remove(k) { try { localStorage.removeItem(k); } catch { /* stockage indisponible */ } },
  };

  // Copie locale des listes, modifications en attente comprises : enregistrée peu après
  // chaque changement, et tout de suite quand l'appli passe en arrière-plan.
  let saveTimer = null;
  function saveState() {
    clearTimeout(saveTimer);
    saveTimer = null;
    if (loaded) store.set(STATE_KEY, JSON.stringify(lists));
  }
  function scheduleSave() {
    if (!saveTimer) saveTimer = setTimeout(saveState, 500);
  }

  function randomHex() {
    const b = new Uint8Array(16);
    crypto.getRandomValues(b);
    return [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
  }

  // Même calcul que creationID côté serveur : l'identifiant est connu avant la réponse.
  async function creationID(nonce) {
    const data = new TextEncoder().encode('listes-id\0' + ME + '\0' + nonce);
    const h = crypto.subtle ? new Uint8Array(await crypto.subtle.digest('SHA-256', data)) : sha256(data);
    return [...h.slice(0, 16)].map((x) => x.toString(16).padStart(2, '0')).join('');
  }

  // SHA-256 (FIPS 180-4) pour les pages servies en HTTP simple (LISTES_INSECURE_HTTP) : le
  // navigateur n'y fournit pas crypto.subtle, réservé aux contextes sécurisés.
  const K256 = [];
  {
    const frac = (x) => ((x - Math.floor(x)) * 2 ** 32) >>> 0;
    for (let n = 2; K256.length < 64; n++) {
      let prime = true;
      for (let d = 2; d * d <= n; d++) if (n % d === 0) { prime = false; break; }
      if (prime) K256.push(frac(Math.cbrt(n)));
    }
  }
  function sha256(bytes) {
    const len = bytes.length;
    const padded = new Uint8Array(((len + 9 + 63) >> 6) << 6);
    padded.set(bytes);
    padded[len] = 0x80;
    const dv = new DataView(padded.buffer);
    dv.setUint32(padded.length - 8, Math.floor(len / 2 ** 29));
    dv.setUint32(padded.length - 4, (len << 3) >>> 0);
    const H = [0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19];
    const w = new Uint32Array(64);
    const ror = (x, n) => (x >>> n) | (x << (32 - n));
    for (let off = 0; off < padded.length; off += 64) {
      for (let i = 0; i < 16; i++) w[i] = dv.getUint32(off + i * 4);
      for (let i = 16; i < 64; i++) {
        const s0 = ror(w[i - 15], 7) ^ ror(w[i - 15], 18) ^ (w[i - 15] >>> 3);
        const s1 = ror(w[i - 2], 17) ^ ror(w[i - 2], 19) ^ (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
      }
      let [a, b, c, d, e, f, g, h] = H;
      for (let i = 0; i < 64; i++) {
        const t1 = (h + (ror(e, 6) ^ ror(e, 11) ^ ror(e, 25)) + ((e & f) ^ (~e & g)) + K256[i] + w[i]) >>> 0;
        const t2 = ((ror(a, 2) ^ ror(a, 13) ^ ror(a, 22)) + ((a & b) ^ (a & c) ^ (b & c))) >>> 0;
        h = g; g = f; f = e; e = (d + t1) >>> 0;
        d = c; c = b; b = a; a = (t1 + t2) >>> 0;
      }
      [a, b, c, d, e, f, g, h].forEach((x, i) => { H[i] = (H[i] + x) >>> 0; });
    }
    const out = new Uint8Array(32);
    const ov = new DataView(out.buffer);
    H.forEach((x, i) => ov.setUint32(i * 4, x));
    return out;
  }

  const byId = (id) => lists.find((l) => l.id === id);
  const currentList = () => byId(current);
  const isMine = (l) => l.owner_id === ME;
  // Ordre des onglets et du panneau : les miennes, puis celles partagées avec moi.
  const sortedLists = () => [...lists].sort((a, b) => (isMine(b) - isMine(a)) || (a.position - b.position));

  let toastTimer;
  function toast(msg) {
    el.toast.textContent = msg;
    el.toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.toast.hidden = true; }, 4000);
  }

  // ---------------------------------------------------------------------------
  // Annuler une suppression : les 5 dernières, une à une, par le bouton du bandeau
  // (qui montre alors la précédente) ou Ctrl+Z / ⌘Z hors d'un champ de saisie. Les
  // éléments sont recréés à leur place, sous de nouveaux identifiants ; une liste
  // supprimée revient avec ses éléments.

  const UNDO_MAX = 5;
  const undos = []; // {label, listId, items} ou {label, list}
  let undoTimer;
  let undoing = Promise.resolve(); // une annulation à la fois : la suivante voit les identifiants recréés

  function remember(entry) {
    undos.push(entry);
    if (undos.length > UNDO_MAX) undos.shift();
    showUndo();
  }

  const quote = (s) => t('quote', { s });
  const itemsLabel = (items) => (items.length > 1 ? t('itemsDeleted', { n: items.length })
    : t(items[0].category ? 'categoryDeleted' : 'itemDeleted', { q: quote(items[0].text) }));

  function showUndo() {
    clearTimeout(undoTimer);
    const e = undos.at(-1);
    el.undo.hidden = !e;
    if (!e) return;
    el.undoText.textContent = e.label;
    undoTimer = setTimeout(() => { el.undo.hidden = true; }, 8000);
  }

  function undoLast() {
    undoing = undoing.then(async () => {
      const e = undos.pop();
      if (e) await (e.list ? restoreList(e.list) : restoreItems(e.listId, e.items));
      showUndo();
    });
  }

  // Même calcul que restoreItems côté serveur : du plus bas au plus haut, chaque élément
  // reprend sa position en décalant ceux qui y sont ou après. Envoi par paquets sous la
  // taille maximale d'une requête, dans le même ordre.
  async function restoreItems(listId, snaps) {
    const l = byId(listId);
    if (!l) { toast(t('listGone')); return; }
    const made = [];
    for (const s of snaps) {
      if (l.items.some((x) => x.id === s.id)) continue; // encore là (décoché par un autre entre-temps)
      const nonce = randomHex();
      const body = { nonce, text: s.text, checked: s.checked, category: !!s.category, position: s.position };
      made.push({ body, it: { ...s, id: await creationID(nonce), list_id: l.id, updated_at: Date.now() } });
    }
    if (!made.length) return;
    made.sort((a, b) => b.body.position - a.body.position);
    made.forEach(({ it }) => {
      l.items.forEach((x) => { if (x.position >= it.position) x.position++; });
      l.items.push(it);
    });
    if (l.id !== current) open(l.id); else renderItems(l);
    const bytes = (x) => new TextEncoder().encode(JSON.stringify(x)).length;
    let chunk = [];
    let size = 0;
    const post = () => {
      if (chunk.length) send('POST', '/api/lists/' + l.id + '/restore', { items: chunk.map((m) => m.body) }, chunk.map((m) => m.it.id));
      chunk = [];
      size = 0;
    };
    made.forEach((m) => {
      const n = bytes(m.body) + 1;
      if (chunk.length === 200 || size + n > 12000) post();
      chunk.push(m);
      size += n;
    });
    post();
  }

  async function restoreList(snap) {
    const nonce = randomHex();
    const id = await creationID(nonce);
    const { items, ...meta } = snap;
    lists.push({ ...meta, id, items: [] });
    undos.forEach((e) => { if (e.listId === snap.id) e.listId = id; });
    send('POST', '/api/lists', { nonce, name: snap.name, color: snap.color, shared: snap.shared, position: snap.position }, [id]);
    open(id);
    await restoreItems(id, items);
  }

  // ---------------------------------------------------------------------------
  // File d'attente : chaque modification part dans l'ordre ; en cas de coupure, elle
  // reste dans la file (sauvegardée localement) et repart au retour du réseau.

  let queue = [];
  try { queue = JSON.parse(store.get(QUEUE_KEY) || '[]'); } catch { queue = []; }
  const pending = new Map(); // id d'élément ou de liste -> opérations en attente
  const trackPending = (ids, d) => ids.forEach((id) => {
    const n = (pending.get(id) || 0) + d;
    if (n > 0) pending.set(id, n); else pending.delete(id);
  });
  queue.forEach((op) => trackPending(op.ids, 1));
  const saveQueue = () => store.set(QUEUE_KEY, JSON.stringify(queue));

  function send(method, url, body, ids) {
    queue.push({ method, url, body, ids });
    trackPending(ids, 1);
    saveQueue();
    scheduleSave();
    flush();
  }

  let flushing = false;
  let retryTimer = null;
  let resyncWanted = false;

  async function flush() {
    if (flushing) return;
    flushing = true;
    clearTimeout(retryTimer);
    try {
      while (queue.length) {
        const op = queue[0];
        let res;
        // Réseau qui ne répond pas (supermarché) : bandeau au bout de 4 s, abandon à 15 s.
        // Renvoyer une opération est sans risque : toutes sont idempotentes.
        const ctl = new AbortController();
        const slow = setTimeout(() => setOffline(true), 4000);
        const abort = setTimeout(() => ctl.abort(), 15000);
        try {
          res = await fetch(op.url, {
            method: op.method,
            headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': CSRF },
            body: op.body === undefined ? undefined : JSON.stringify(op.body),
            credentials: 'same-origin',
            signal: ctl.signal,
          });
        } catch {
          return retryLater();
        } finally {
          clearTimeout(slow);
          clearTimeout(abort);
        }
        if (res.status === 401) { location.href = '/login'; return; }
        // Serveur injoignable derrière le reverse proxy (machine éteinte, réseau coupé) : comme hors ligne.
        if (res.status === 502 || res.status === 503 || res.status === 504) return retryLater();
        setOffline(false);
        queue.shift();
        trackPending(op.ids, -1);
        saveQueue();
        if (!res.ok) {
          // Refus définitif (404 : la liste n'est plus visible, 403, 400…) : on abandonne
          // l'opération et on recharge l'état du serveur.
          if (res.status !== 404) {
            const err = await res.json().catch(() => ({}));
            toast(err.error || t('refused'));
          }
          resyncWanted = true;
        }
      }
      if (resyncWanted) { resyncWanted = false; await resync(); }
    } finally {
      flushing = false;
    }
  }

  function retryLater() {
    setOffline(true);
    clearTimeout(retryTimer);
    retryTimer = setTimeout(flush, 5000);
  }

  function setOffline(off) {
    el.offline.hidden = !off;
  }

  // ---------------------------------------------------------------------------
  // État du serveur

  // Recharge l'état du serveur, en gardant par-dessus les modifications locales encore
  // dans la file (elles finiront par arriver et primeront : dernière écriture gagnante).
  async function resync() {
    let res;
    try {
      res = await fetch('/api/state', { credentials: 'same-origin' });
    } catch {
      setOffline(true);
      return;
    }
    if (res.status === 401) { location.href = '/login'; return; }
    if (!res.ok) return;
    const data = await res.json();
    const gone = new Set();
    queue.forEach((op) => {
      if (op.method === 'DELETE' || op.url.endsWith('/clear-checked')) op.ids.forEach((id) => gone.add(id));
    });
    const old = new Map(lists.map((l) => [l.id, l]));
    const next = data.lists.filter((l) => !gone.has(l.id)).map((l) => {
      const items = (l.items || []).filter((it) => !gone.has(it.id));
      const local = old.get(l.id);
      if (!local) return { ...l, items };
      const meta = pending.has(l.id) ? { name: local.name, color: local.color, shared: local.shared } : {};
      return { ...l, ...meta, items: mergeItems(local.items, items) };
    });
    old.forEach((l, id) => { if (pending.has(id) && !next.some((x) => x.id === id)) next.push(l); });
    lists = next;
    loaded = true;
    if (!byId(current)) current = pickInitial();
    render();
  }

  function pickInitial() {
    const last = store.get(LAST_KEY);
    if (last && byId(last)) return last;
    return lists.length ? lists[0].id : null;
  }

  function open(id) {
    current = id;
    if (id) store.set(LAST_KEY, id);
    hideConfirm();
    render();
  }

  // ---------------------------------------------------------------------------
  // Temps réel

  // Le serveur envoie un « ping » toutes les 20 s : sans nouvelles depuis 45 s, la
  // connexion est morte sans erreur visible (réseau coupé en silence) ; on la rouvre.
  let es = null;
  let lastEvent = Date.now();
  function connect() {
    if (es) es.close();
    lastEvent = Date.now();
    es = new EventSource('/api/events');
    es.onopen = () => { lastEvent = Date.now(); setOffline(false); flush(); resync(); };
    es.addEventListener('ping', () => { lastEvent = Date.now(); });
    es.onerror = () => {
      setOffline(true);
      // Fermé définitivement (session expirée…) : on vérifie, puis on rouvre.
      if (es.readyState === EventSource.CLOSED) setTimeout(() => resync().then(connect), 3000);
    };
    es.addEventListener('list', (e) => onList(JSON.parse(e.data)));
    ['list', 'list-removed', 'item', 'item-removed', 'items-removed', 'items-moved'].forEach((n) => es.addEventListener(n, () => { lastEvent = Date.now(); }));
    es.addEventListener('list-removed', (e) => onListRemoved(JSON.parse(e.data)));
    es.addEventListener('item', (e) => onItem(JSON.parse(e.data)));
    es.addEventListener('item-removed', (e) => onItemRemoved(JSON.parse(e.data)));
    es.addEventListener('items-removed', (e) => onItemsRemoved(JSON.parse(e.data)));
    es.addEventListener('items-moved', (e) => onItemsMoved(JSON.parse(e.data)));
  }

  function onList(l) {
    const local = byId(l.id);
    if (!local) {
      lists.push({ ...l, items: l.items || [] });
    } else {
      const items = l.items ? mergeItems(local.items, l.items) : local.items;
      if (pending.has(l.id)) local.items = items; else Object.assign(local, l, { items });
    }
    render();
  }

  // Remplace les éléments par ceux du serveur, sauf ceux qui ont une modification locale en attente.
  function mergeItems(localItems, serverItems) {
    const mine = new Map(localItems.filter((it) => pending.has(it.id)).map((it) => [it.id, it]));
    const out = serverItems.map((it) => mine.get(it.id) || it);
    mine.forEach((it) => { if (!out.some((o) => o.id === it.id)) out.push(it); });
    return out;
  }

  function onListRemoved({ id, reason }) {
    const l = byId(id);
    if (!l) return;
    lists = lists.filter((x) => x.id !== id);
    if (current === id) {
      open(lists.length ? lists[0].id : null);
      toast(t(reason === 'unshared' ? 'unshared' : 'deleted'));
    } else {
      render();
    }
  }

  function onItem(it) {
    if (pending.has(it.id)) return;
    const l = byId(it.list_id);
    if (!l) return;
    const i = l.items.findIndex((x) => x.id === it.id);
    if (i >= 0) l.items[i] = it; else l.items.push(it);
    if (l.id === current) renderItems(l);
  }

  function onItemRemoved({ id, list_id: listId }) {
    if (pending.has(id)) return;
    const l = byId(listId);
    if (!l) return;
    l.items = l.items.filter((x) => x.id !== id);
    if (l.id === current) renderItems(l);
  }

  function onItemsRemoved({ list_id: listId, ids }) {
    const l = byId(listId);
    if (!l) return;
    const gone = new Set(ids.filter((id) => !pending.has(id)));
    l.items = l.items.filter((x) => !gone.has(x.id));
    if (l.id === current) renderItems(l);
  }

  function onItemsMoved({ list_id: listId, items }) {
    const l = byId(listId);
    if (!l) return;
    const pos = new Map(items.map((x) => [x.id, x.position]));
    l.items.forEach((it) => { if (pos.has(it.id) && !pending.has(it.id)) it.position = pos.get(it.id); });
    if (l.id === current) renderItems(l);
  }

  // ---------------------------------------------------------------------------
  // Rendu (textContent uniquement : aucun HTML venant des données)

  function render() {
    scheduleSave();
    renderTabs();
    if (el.sheet.open) renderSheet();
    const l = currentList();
    el.list.hidden = !l;
    el.empty.hidden = !!l || !loaded;
    if (!l) return;
    el.list.className = 'list c-' + l.color;
    if (document.activeElement !== el.title) el.title.value = l.name;
    el.switcherName.textContent = l.name;
    el.switcher.setAttribute('aria-label', t('switcherLabel', { name: l.name }));
    const mine = isMine(l);
    el.share.checked = l.shared;
    el.share.disabled = !mine;
    el.ownerTools.hidden = !mine;
    el.ownerNote.hidden = mine;
    el.ownerNote.textContent = mine ? '' : t('ownerNote', { owner: l.owner_name });
    document.querySelectorAll('input[name="color"]').forEach((r) => { r.checked = r.value === l.color; });
    renderItems(l);
  }

  function renderTabs() {
    el.tabs.replaceChildren(...sortedLists().map((l) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'tab';
      b.dataset.id = l.id;
      b.setAttribute('aria-current', String(l.id === current));
      const dot = document.createElement('span');
      dot.className = 'dot c-' + l.color;
      const name = document.createElement('span');
      name.className = 'name';
      name.textContent = l.name;
      b.title = l.name;
      b.append(dot, name);
      if (!isMine(l)) {
        const from = document.createElement('span');
        from.className = 'from';
        from.textContent = ' · ' + t('from', { owner: l.owner_name });
        b.append(from);
      }
      if (l.shared) {
        const mark = document.createElement('span');
        mark.className = 'mark';
        mark.textContent = '⇄';
        mark.title = t('shared');
        mark.setAttribute('aria-label', t('sharedAria'));
        b.append(mark);
      }
      return b;
    }));
    const active = el.tabs.querySelector('[aria-current="true"]');
    if (active) active.scrollIntoView({ block: 'nearest', inline: 'nearest' });
    updateTabEdges();
  }

  function updateTabEdges() {
    const t = el.tabs;
    const left = t.scrollLeft > 2;
    const right = t.scrollLeft + t.clientWidth < t.scrollWidth - 2;
    t.classList.toggle('more-left', left);
    t.classList.toggle('more-right', right);
    el.tabsPrev.hidden = !left;
    el.tabsNext.hidden = !right;
  }

  // Panneau des listes : toutes d'un coup d'œil, avec le nombre d'éléments à faire.
  function renderSheet() {
    const rows = [];
    let shared = false;
    sortedLists().forEach((l) => {
      if (!isMine(l) && !shared) {
        shared = true;
        const h = document.createElement('p');
        h.className = 'sheet-group';
        h.textContent = t('sharedWithMe');
        rows.push(h);
      }
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'sheet-item';
      b.dataset.id = l.id;
      b.setAttribute('aria-current', String(l.id === current));
      const dot = document.createElement('span');
      dot.className = 'dot c-' + l.color;
      const name = document.createElement('span');
      name.className = 'name';
      name.textContent = l.name;
      b.append(dot, name);
      if (!isMine(l)) {
        const from = document.createElement('span');
        from.className = 'from';
        from.textContent = t('from', { owner: l.owner_name });
        b.append(from);
      }
      if (l.shared) {
        const mark = document.createElement('span');
        mark.className = 'mark';
        mark.textContent = '⇄';
        mark.setAttribute('aria-label', t('sharedAria'));
        b.append(mark);
      }
      const n = l.items.filter((it) => !it.category && !it.checked).length;
      const count = document.createElement('span');
      count.className = 'count';
      count.textContent = n || '';
      if (n) count.setAttribute('aria-label', t('todo', { n }));
      b.append(count);
      rows.push(b);
    });
    el.sheetLists.replaceChildren(...rows);
    updateSheetEdges();
  }

  function openSheet() {
    renderSheet();
    el.sheet.showModal();
    const cur = el.sheetLists.querySelector('[aria-current="true"]');
    if (cur) cur.focus();
    updateSheetEdges();
  }

  // Listes cachées au-dessus ou en dessous : fondu sur ce bord, et une pastille qui dit
  // combien il en reste (la barre de défilement d'un téléphone n'apparaît qu'au geste).
  function updateSheetEdges() {
    const s = el.sheetLists;
    const box = s.getBoundingClientRect();
    let above = 0;
    let below = 0;
    s.querySelectorAll('.sheet-item').forEach((b) => {
      const r = b.getBoundingClientRect();
      if (r.top < box.top - r.height / 2) above++;
      else if (r.bottom > box.bottom + r.height / 2) below++;
    });
    s.classList.toggle('more-top', s.scrollTop > 2);
    s.classList.toggle('more-bottom', s.scrollTop + s.clientHeight < s.scrollHeight - 2);
    const label = (n) => t(n > 1 ? 'otherMany' : 'otherOne', { n });
    el.sheetUp.hidden = !above;
    el.sheetUp.textContent = label(above);
    el.sheetUp.setAttribute('aria-label', t('seeAbove', { label: label(above) }));
    el.sheetDown.hidden = !below;
    el.sheetDown.textContent = label(below);
    el.sheetDown.setAttribute('aria-label', t('seeBelow', { label: label(below) }));
  }
  const pageSheet = (dir) => el.sheetLists.scrollBy({ top: dir * el.sheetLists.clientHeight * 0.7, behavior: reduceMotion ? 'auto' : 'smooth' });

  // Le nom s'édite à la place du sélecteur, le temps de la saisie.
  function editTitle() {
    el.listHead.classList.add('renaming');
    el.title.focus();
    el.title.select();
  }

  // Cochés en bas, en gris. Les lignes existantes sont réutilisées pour ne pas
  // perdre le focus d'un texte en cours de modification.
  function renderItems(l) {
    scheduleSave();
    if (drag) return; // endDrag rend la liste au lâcher
    const { ordered, done } = displayOrder(l);
    const existing = new Map([...el.items.children].map((li) => [li.dataset.id, li]));
    let ref = el.items.firstChild;
    let grouped = false; // chaque catégorie et ses éléments forment une bande (classe grouped)
    ordered.forEach((it, i) => {
      const li = existing.get(it.id) || makeRow(it.id);
      existing.delete(it.id);
      if (it.category) grouped = true;
      const next = ordered[i + 1];
      li.classList.toggle('grouped', grouped);
      li.classList.toggle('group-end', grouped && (!next || !!next.category));
      li.classList.toggle('category', !!it.category);
      li.classList.toggle('done', it.category ? done.has(it.id) : it.checked);
      li.querySelector('.check input').checked = it.checked;
      const text = li.querySelector('.text');
      text.setAttribute('aria-label', t(it.category ? 'categoryName' : 'itemText'));
      if (document.activeElement !== text && text.value !== it.text) text.value = it.text;
      fitText(text);
      if (li !== ref) el.items.insertBefore(li, ref); else ref = ref.nextSibling;
    });
    existing.forEach((li) => li.remove());
    // Un élément coché suffit pour proposer de tout décocher (courses récurrentes) ;
    // sinon on propose de tout cocher.
    const anyChecked = l.items.some((it) => it.checked);
    el.checkedTools.hidden = !l.items.some((it) => !it.category);
    el.clear.hidden = !anyChecked;
    el.uncheck.textContent = t(anyChecked ? 'uncheckAll' : 'checkAll');
  }

  // Ordre affiché : les éléments placés avant la première catégorie, puis chaque
  // catégorie suivie des siens ; dans chaque groupe, les non cochés d'abord. Une
  // catégorie est rayée (done) quand elle a des éléments et qu'ils sont tous cochés.
  function displayOrder(l) {
    const groups = [{ head: null, items: [] }];
    [...l.items].sort((a, b) => a.position - b.position).forEach((it) => {
      if (it.category) groups.push({ head: it, items: [] });
      else groups[groups.length - 1].items.push(it);
    });
    const ordered = [];
    const done = new Set();
    for (const { head, items } of groups) {
      if (head) {
        ordered.push(head);
        if (items.length && items.every((it) => it.checked)) done.add(head.id);
      }
      ordered.push(...items.sort((a, b) => a.checked - b.checked));
    }
    return { ordered, done };
  }

  // Hauteur du texte d'un élément : celle de son contenu, plafonnée à 3 lignes (CSS).
  // field-sizing le fait seul là où il existe ; sinon on mesure. Au-delà de 3 lignes,
  // la dernière s'estompe (classe clipped) tant qu'on n'édite pas.
  const autoSizing = CSS.supports('field-sizing', 'content');
  function fitText(ta) {
    if (!autoSizing) {
      ta.style.height = 'auto';
      ta.style.height = ta.scrollHeight + 'px';
    }
    ta.classList.toggle('clipped', document.activeElement !== ta && ta.scrollHeight > ta.clientHeight + 1);
  }
  const fitAll = () => el.items.querySelectorAll('.text').forEach(fitText);

  function makeRow(id) {
    const li = document.createElement('li');
    li.className = 'item';
    li.dataset.id = id;
    const handle = document.createElement('button');
    handle.type = 'button';
    handle.className = 'handle';
    handle.setAttribute('aria-label', t('move'));
    const label = document.createElement('label');
    label.className = 'check';
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.setAttribute('aria-label', t('check'));
    label.append(box);
    // Zone de texte qui passe à la ligne (jusqu'à 3 lignes), sans retour chariot possible.
    const text = document.createElement('textarea');
    text.className = 'text';
    text.rows = 1;
    text.maxLength = el.newItem.maxLength;
    text.autocomplete = 'off';
    text.enterKeyHint = 'done';
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'remove';
    remove.textContent = '−';
    remove.setAttribute('aria-label', t('remove'));
    li.append(handle, label, text, remove);
    return li;
  }

  // ---------------------------------------------------------------------------
  // Actions

  async function addItem({ text, category }) {
    const l = currentList();
    if (!l) return;
    const nonce = randomHex();
    const id = await creationID(nonce);
    const pos = l.items.reduce((m, it) => Math.max(m, it.position), 0) + 1;
    l.items.push({ id, list_id: l.id, text, checked: false, category, position: pos, updated_at: Date.now() });
    renderItems(l);
    send('POST', '/api/lists/' + l.id + '/items', { nonce, text, category }, [id]);
  }

  // « # Fruits » (ou « ## Fruits », titre Markdown collé) crée la catégorie « Fruits ».
  function parseEntry(line) {
    const m = /^#+\s*(.*)$/.exec(line);
    return m ? { text: m[1].trim(), category: true } : { text: line, category: false };
  }

  function findItem(id) {
    const l = currentList();
    return l && l.items.find((x) => x.id === id);
  }

  function setChecked(id, checked) {
    const it = findItem(id);
    if (!it || it.category || it.checked === checked) return;
    it.checked = checked;
    renderItems(currentList());
    send('PATCH', '/api/items/' + id, { checked }, [id]);
  }

  function setText(id, value) {
    const it = findItem(id);
    if (!it) return;
    const text = value.trim();
    if (text === it.text) return;
    const l = currentList();
    if (text === '') {
      remember({ label: itemsLabel([it]), listId: l.id, items: [{ ...it }] });
      l.items = l.items.filter((x) => x.id !== id);
      send('DELETE', '/api/items/' + id, undefined, [id]);
    } else {
      it.text = text;
      send('PATCH', '/api/items/' + id, { text }, [id]);
    }
    renderItems(l);
  }

  // Même calcul que moveItem côté serveur : l'élément (avec ses éléments si c'est une
  // catégorie) passe juste avant `before` (ou en dernier), puis les positions sont renumérotées.
  function moveItem(id, before) {
    const l = currentList();
    if (!l) return;
    const order = [...l.items].sort((a, b) => a.position - b.position);
    const start = order.findIndex((x) => x.id === id);
    if (start < 0) return;
    let end = start + 1;
    if (order[start].category) while (end < order.length && !order[end].category) end++;
    const block = order.splice(start, end - start);
    const at = before ? order.findIndex((x) => x.id === before && (!block[0].category || x.category)) : order.length;
    if (at < 0) return;
    order.splice(at, 0, ...block);
    order.forEach((x, i) => { x.position = i + 1; });
    send('POST', '/api/items/' + id + '/move', { before: before || '' }, [id]);
  }

  const isHead = (li) => li.classList.contains('category');
  const isDone = (li) => li.classList.contains('done');

  // Cible d'un déplacement : la ligne suivante de même rang dans l'ordre affiché, c'est-à-dire
  // la catégorie suivante, ou pour un élément le suivant de même état (coché ou non) s'il
  // vient avant. Le placer juste avant (en position) le remet exactement là où il est affiché.
  function nextTarget(li) {
    for (let x = li.nextElementSibling; x; x = x.nextElementSibling) {
      if (isHead(x) || (!isHead(li) && isDone(x) === isDone(li))) return x.dataset.id;
    }
    return '';
  }

  // Catégorie tenue : ses éléments se replient (classe carried) et voyagent avec elle.
  function carry(li, on) {
    const kids = [];
    for (let x = li.nextElementSibling; x && !isHead(x); x = x.nextElementSibling) {
      x.classList.toggle('carried', on);
      kids.push(x);
    }
    return kids;
  }

  // Au doigt, le repli se voit : les éléments glissent sous le titre en s'aplatissant
  // (classe folding : visibles le temps de l'animation), puis se déplient au lâcher, à
  // leur nouvelle place. Le titre tenu montre alors une pile de cartes (classe stack).
  const folded = (h) => [{ height: h + 'px', opacity: 1, transform: 'none' }, { height: '0px', opacity: 0, transform: 'translateY(-16px)' }];
  function fold(li) {
    const heights = [];
    for (let x = li.nextElementSibling; x && !isHead(x); x = x.nextElementSibling) heights.push(x.offsetHeight);
    const kids = carry(li, true);
    li.classList.toggle('stack', kids.length > 0);
    if (reduceMotion) return;
    kids.forEach((x, i) => {
      x.classList.add('folding');
      const a = x.animate(folded(heights[i]), { duration: 220, easing: EASE, fill: 'forwards' });
      a.onfinish = () => { x.classList.remove('folding'); a.cancel(); };
    });
  }
  function unfold(li) {
    li.classList.remove('stack');
    const kids = carry(li, false);
    kids.forEach((x) => {
      x.getAnimations().forEach((a) => a.cancel());
      x.classList.remove('folding');
    });
    return kids;
  }
  // Après le rendu : les éléments, à leur nouvelle place, s'ouvrent depuis le titre.
  function unfoldAnimation(kids) {
    if (reduceMotion) return;
    kids.forEach((x) => {
      const h = x.offsetHeight;
      x.classList.add('folding');
      const a = x.animate(folded(h).reverse(), { duration: 220, easing: EASE });
      a.onfinish = a.oncancel = () => x.classList.remove('folding');
    });
  }
  const blockEnd = (li) => {
    let e = li;
    while (e.nextElementSibling && e.nextElementSibling.classList.contains('carried')) e = e.nextElementSibling;
    return e;
  };

  // Lignes que li doit franchir pour avancer d'un cran (dir = 1 vers le bas, -1 vers le
  // haut), dans l'ordre du document, ou null si elle ne peut pas aller plus loin.
  // Un élément ne quitte jamais sa zone (non cochés en haut du groupe, cochés en bas) :
  // il échange sa place avec un voisin de même état, ou saute d'un coup les lignes de
  // l'autre état et un titre de catégorie pour entrer dans le groupe voisin. Une catégorie
  // (repliée) franchit une catégorie entière, sans jamais passer au-dessus des éléments
  // placés avant la première catégorie.
  function jumpOver(li, dir) {
    const step = (x) => (dir > 0 ? x.nextElementSibling : x.previousElementSibling);
    const run = [];
    if (isHead(li)) {
      for (let x = step(dir > 0 ? blockEnd(li) : li); x; x = step(x)) {
        if (dir > 0 && isHead(x) && run.length) break;
        run.push(x);
        if (dir < 0 && isHead(x)) return run.reverse();
      }
      return dir > 0 && run.length ? run : null;
    }
    let crossed = false;
    for (let x = step(li); x; x = step(x)) {
      if (isHead(x)) {
        if (crossed) break;
        crossed = true;
      } else if (isDone(x) === isDone(li)) {
        if (!run.length) return [x];
        break;
      } else if (crossed && isDone(li) === dir < 0) {
        break; // groupe voisin atteint : un non coché s'arrête en haut, un coché en bas
      }
      run.push(x);
    }
    return crossed ? (dir > 0 ? run : run.reverse()) : null;
  }

  // Après un déplacement dans le DOM : la ligne suivante de même rang donne la cible.
  function commitRowMove(li, beforeAtStart) {
    const before = nextTarget(li);
    if (before === beforeAtStart) return false;
    moveItem(li.dataset.id, before);
    return true;
  }

  // Glisser au doigt ou à la souris (événements pointer), comme Keep : la ligne tenue
  // suit le pointeur au pixel près, soulevée avec une ombre ; les lignes qu'elle
  // pousse glissent jusqu'à leur nouvelle place ; au lâcher, elle se pose en douceur.
  // Les mouvements passent par le CSSOM et la Web Animations API, autorisés par la
  // CSP (seuls les attributs de style inline sont interdits).
  const EASE = 'cubic-bezier(.2, .8, .2, 1)';
  const LIFT = ' scale(1.02)';
  const reduceMotion = matchMedia('(prefers-reduced-motion: reduce)').matches;

  // FLIP : mesure, modifie le DOM, puis fait glisser chaque ligne déplacée depuis
  // son ancienne place visuelle jusqu'à la nouvelle.
  function flip(nodes, mutate) {
    const before = new Map(nodes.map((n) => [n, n.getBoundingClientRect().top]));
    mutate();
    if (reduceMotion) return;
    nodes.forEach((n) => {
      const d = before.get(n) - n.getBoundingClientRect().top;
      if (Math.abs(d) > 0.5) {
        n.animate([{ transform: 'translateY(' + d + 'px)' }, { transform: 'none' }], { duration: 200, easing: EASE });
      }
    });
  }

  let drag = null;
  function startDrag(e, handle) {
    const li = handle.closest('.item');
    if (isHead(li)) fold(li);
    const top = li.getBoundingClientRect().top;
    drag = { li, y: e.clientY, grab: e.clientY - top, dy: 0, beforeAtStart: nextTarget(li) };
    // Emplacement d'atterrissage : un aplat plus sombre à la place que prendra la ligne.
    drag.slot = document.createElement('div');
    drag.slot.className = 'drop-slot instant';
    el.list.append(drag.slot);
    placeSlot();
    requestAnimationFrame(() => { if (drag) drag.slot.classList.remove('instant'); });
    handle.setPointerCapture(e.pointerId);
    li.getAnimations().forEach((a) => a.cancel());
    li.classList.add('dragging');
    document.body.classList.add('is-dragging');
    if (navigator.vibrate) navigator.vibrate(8);
    requestAnimationFrame(dragFrame);
  }

  // Ce sont les voisines qui changent de place : la ligne tenue ne quitte jamais le
  // DOM, sinon le navigateur perdrait la capture du pointeur.
  function dragTo() {
    const li = drag.li;
    // Milieu des lignes à franchir : la ligne tenue y passe quand son centre le dépasse.
    const mid = (rows) => (rows[0].offsetTop + rows.at(-1).offsetTop + rows.at(-1).offsetHeight) / 2;
    const top = drag.y - el.items.getBoundingClientRect().top - drag.grab; // haut voulu, repère de la liste
    const center = top + li.offsetHeight / 2;
    const up = jumpOver(li, -1);
    const down = jumpOver(li, 1);
    if (up && center < mid(up)) flip(up, () => blockEnd(li).after(...up));
    else if (down && center > mid(down)) flip(down, () => li.before(...down));
    // La ligne reste sous le doigt, quelle que soit sa place dans la liste.
    drag.dy = top - li.offsetTop;
    li.style.transform = 'translateY(' + drag.dy + 'px)' + LIFT;
    placeSlot();
  }

  // La ligne tenue garde sa place dans le DOM (elle n'est que décalée visuellement) :
  // sa position de mise en page est exactement celle où elle atterrira.
  function placeSlot() {
    const { li, slot } = drag;
    const ul = el.items;
    slot.style.width = ul.offsetWidth + 'px';
    slot.style.height = li.offsetHeight + 'px';
    slot.style.transform = 'translate(' + ul.offsetLeft + 'px, ' + (ul.offsetTop + li.offsetTop) + 'px)';
  }

  function dragFrame() {
    if (!drag) return;
    const edge = 90;
    if (drag.y < edge) window.scrollBy(0, -Math.ceil((edge - drag.y) / 6));
    else if (drag.y > innerHeight - edge) window.scrollBy(0, Math.ceil((drag.y - innerHeight + edge) / 6));
    dragTo();
    requestAnimationFrame(dragFrame);
  }

  function endDrag(commit) {
    if (!drag) return;
    const { li, beforeAtStart, dy, slot } = drag;
    drag = null;
    const kids = isHead(li) ? unfold(li) : [];
    if (reduceMotion) slot.remove();
    else slot.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 180, easing: EASE }).finished.then(() => slot.remove());
    li.style.transform = '';
    li.classList.remove('dragging'); // l'ombre s'estompe (transition CSS)
    document.body.classList.remove('is-dragging');
    if (!reduceMotion) {
      li.animate([{ transform: 'translateY(' + dy + 'px)' + LIFT }, { transform: 'none' }], { duration: 180, easing: EASE });
    }
    if (commit) commitRowMove(li, beforeAtStart);
    renderItems(currentList()); // replace aussi les éléments d'une catégorie déplacée
    unfoldAnimation(kids);
  }

  function removeItem(id) {
    const l = currentList();
    const it = l && l.items.find((x) => x.id === id);
    if (!it) return;
    remember({ label: itemsLabel([it]), listId: l.id, items: [{ ...it }] });
    l.items = l.items.filter((x) => x.id !== id);
    send('DELETE', '/api/items/' + id, undefined, [id]);
    renderItems(l);
  }

  async function newList() {
    const nonce = randomHex();
    const id = await creationID(nonce);
    const mine = lists.filter(isMine);
    const color = COLORS[mine.length % COLORS.length];
    const position = mine.reduce((m, l) => Math.max(m, l.position), 0) + 1;
    lists.push({ id, owner_id: ME, owner_name: document.body.dataset.name, name: DEFAULT_LIST_NAME,
      color, shared: false, position, items: [] });
    send('POST', '/api/lists', { nonce, name: DEFAULT_LIST_NAME, color }, [id]);
    open(id);
    editTitle();
  }

  function patchList(patch) {
    const l = currentList();
    if (!l) return;
    Object.assign(l, patch);
    send('PATCH', '/api/lists/' + l.id, patch, [l.id]);
    render();
  }

  function hideConfirm() {
    el.confirm.hidden = true;
    el.del.hidden = false;
  }

  // ---------------------------------------------------------------------------
  // Événements de l'interface

  el.tabs.addEventListener('click', (e) => {
    const b = e.target.closest('.tab');
    if (b) open(b.dataset.id);
  });
  el.newList.addEventListener('click', newList);
  el.tabs.addEventListener('scroll', updateTabEdges, { passive: true });
  // Souris : la molette verticale fait défiler la barre horizontalement.
  el.tabs.addEventListener('wheel', (e) => {
    const t = el.tabs;
    if (t.scrollWidth <= t.clientWidth || Math.abs(e.deltaX) >= Math.abs(e.deltaY)) return;
    e.preventDefault();
    t.scrollLeft += e.deltaY;
  }, { passive: false });
  const page = (dir) => el.tabs.scrollBy({ left: dir * el.tabs.clientWidth * 0.7, behavior: 'smooth' });
  el.tabsPrev.addEventListener('click', () => page(-1));
  el.tabsNext.addEventListener('click', () => page(1));
  window.addEventListener('resize', () => { updateTabEdges(); fitAll(); });

  // Téléphone : le titre ouvre le panneau des listes ; toucher hors du panneau le ferme.
  el.switcher.addEventListener('click', openSheet);
  el.sheet.addEventListener('click', (e) => { if (e.target === el.sheet) closeSheet(0); });
  el.sheet.addEventListener('cancel', (e) => { e.preventDefault(); closeSheet(0); }); // Échap
  el.sheetLists.addEventListener('scroll', updateSheetEdges, { passive: true });
  el.sheetUp.addEventListener('click', () => pageSheet(-1));
  el.sheetDown.addEventListener('click', () => pageSheet(1));
  el.sheetLists.addEventListener('click', (e) => {
    const b = e.target.closest('.sheet-item');
    if (!b) return;
    el.sheet.close();
    if (b.dataset.id !== current) { open(b.dataset.id); window.scrollTo(0, 0); }
  });
  el.sheetNew.addEventListener('click', () => { el.sheet.close(); newList(); });
  el.sheetRename.addEventListener('click', () => { el.sheet.close(); editTitle(); });
  mobile.addEventListener('change', () => { if (!mobile.matches && el.sheet.open) el.sheet.close(); });

  // Poignée du panneau : tirée vers le bas, elle le replie (il suit le doigt, le fond
  // s'éclaircit) ; lâchée avant le quart de sa hauteur, sans geste vif, il remonte.
  // Un simple toucher ferme aussi.
  let pull = null;
  function resetPull() {
    el.sheet.classList.remove('pulling');
    el.sheet.style.transform = '';
    el.sheet.style.removeProperty('--pull');
  }
  // Le panneau descend depuis `from` (px), puis se ferme.
  function closeSheet(from) {
    if (!el.sheet.open) return;
    if (reduceMotion) { el.sheet.close(); resetPull(); return; }
    el.sheet.classList.add('pulling');
    el.sheet.style.setProperty('--pull', '1');
    const a = el.sheet.animate([{ transform: 'translateY(' + from + 'px)' }, { transform: 'translateY(100%)' }],
      { duration: 200, easing: 'ease-in', fill: 'forwards' });
    a.finished.then(() => { el.sheet.close(); a.cancel(); resetPull(); });
  }
  el.sheetGrip.addEventListener('pointerdown', (e) => {
    if (e.button > 0 || pull) return;
    e.preventDefault();
    el.sheetGrip.setPointerCapture(e.pointerId);
    pull = { id: e.pointerId, y: e.clientY, t: e.timeStamp, dy: 0 };
    el.sheet.classList.add('pulling');
  });
  el.sheetGrip.addEventListener('pointermove', (e) => {
    if (!pull || e.pointerId !== pull.id) return;
    pull.dy = Math.max(0, e.clientY - pull.y);
    el.sheet.style.transform = 'translateY(' + pull.dy + 'px)';
    el.sheet.style.setProperty('--pull', String(Math.min(1, pull.dy / el.sheet.offsetHeight)));
  });
  function endPull(e) {
    if (!pull || e.pointerId !== pull.id) return;
    const { dy, t } = pull;
    pull = null;
    const tap = dy < 6 && e.type === 'pointerup';
    const flick = dy > 30 && e.timeStamp - t < 250;
    if (tap || flick || dy > el.sheet.offsetHeight / 4) closeSheet(dy);
    else resetPull(); // la transition CSS le fait remonter
  }
  el.sheetGrip.addEventListener('pointerup', endPull);
  el.sheetGrip.addEventListener('pointercancel', endPull);
  el.sheetGrip.addEventListener('click', (e) => { if (e.detail === 0) closeSheet(0); }); // clavier

  // Bouton « Ajouter » : visible dès qu'il y a du texte. Le toucher ne retire pas le
  // focus du champ (le clavier du téléphone reste ouvert pour enchaîner les ajouts).
  const syncAddBtn = () => el.add.classList.toggle('has-text', el.newItem.value.trim() !== '');
  el.newItem.addEventListener('input', syncAddBtn);
  el.addBtn.addEventListener('pointerdown', (e) => e.preventDefault());
  el.add.addEventListener('submit', (e) => {
    e.preventDefault();
    const text = el.newItem.value.trim();
    if (!text) return;
    el.newItem.value = '';
    syncAddBtn();
    el.newItem.focus(); // le focus reste dans le champ pour enchaîner
    const entry = parseEntry(text);
    if (entry.text) addItem(entry);
  });

  // Coller plusieurs lignes (une liste copiée d'ailleurs) ajoute un élément par ligne,
  // sans les puces ni cases à cocher du texte d'origine ; une ligne « # … » ajoute une
  // catégorie. Une seule ligne reste dans le champ.
  const splitLines = (s) => s.split(/\r\n|\r|\n/)
    .map((x) => x.trim().replace(/^(?:[-*•◦▪‣–—·]\s+|\[[ xX]?\]\s*)/, '').trim().slice(0, el.newItem.maxLength))
    .filter((x) => x !== '');
  el.newItem.addEventListener('paste', async (e) => {
    const pasted = e.clipboardData && e.clipboardData.getData('text');
    if (!pasted || !/[\r\n]/.test(pasted)) return;
    e.preventDefault();
    const f = el.newItem;
    const lines = splitLines(f.value.slice(0, f.selectionStart) + pasted + f.value.slice(f.selectionEnd));
    if (lines.length < 2) {
      f.value = lines[0] || '';
      syncAddBtn();
      return;
    }
    f.value = '';
    syncAddBtn();
    for (const entry of lines.map(parseEntry)) {
      if (entry.text) await addItem(entry); // l'une après l'autre, pour garder l'ordre
    }
  });

  el.items.addEventListener('change', (e) => {
    if (!e.target.matches('.check input')) return;
    setChecked(e.target.closest('.item').dataset.id, e.target.checked);
  });
  el.items.addEventListener('pointerdown', (e) => {
    const h = e.target.closest('.handle');
    if (!h || e.button > 0 || drag) return;
    e.preventDefault();
    startDrag(e, h);
  });
  el.items.addEventListener('pointermove', (e) => { if (drag) drag.y = e.clientY; });
  el.items.addEventListener('pointerup', () => endDrag(true));
  el.items.addEventListener('pointercancel', () => endDrag(false));
  el.items.addEventListener('lostpointercapture', () => endDrag(true));
  // Clavier : flèches haut et bas sur la poignée.
  el.items.addEventListener('keydown', (e) => {
    const h = e.target.closest('.handle');
    if (!h || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return;
    e.preventDefault();
    const li = h.closest('.item');
    const dir = e.key === 'ArrowUp' ? -1 : 1;
    const beforeAtStart = nextTarget(li);
    if (isHead(li)) carry(li, true);
    const run = jumpOver(li, dir);
    if (run) flip([li, ...run], () => (dir < 0 ? blockEnd(li).after(...run) : li.before(...run)));
    carry(li, false);
    if (!run) return;
    commitRowMove(li, beforeAtStart);
    renderItems(currentList());
    h.focus();
  });
  el.items.addEventListener('click', (e) => {
    const b = e.target.closest('.remove');
    if (b) removeItem(b.closest('.item').dataset.id);
  });
  el.items.addEventListener('keydown', (e) => {
    if (!e.target.matches('.text')) return;
    if (e.key === 'Enter' && !e.isComposing) { e.preventDefault(); e.target.blur(); }
    if (e.key === 'Escape') {
      const it = findItem(e.target.closest('.item').dataset.id);
      if (it) e.target.value = it.text;
      e.target.blur();
    }
  });
  el.items.addEventListener('input', (e) => {
    if (!e.target.matches('.text')) return;
    if (/[\r\n]/.test(e.target.value)) e.target.value = e.target.value.replace(/[\r\n]+/g, ' ');
    fitText(e.target);
  });
  el.items.addEventListener('focusin', (e) => {
    if (e.target.matches('.text')) e.target.classList.remove('clipped');
  });
  el.items.addEventListener('focusout', (e) => {
    if (!e.target.matches('.text')) return;
    requestAnimationFrame(() => { e.target.scrollTop = 0; fitText(e.target); });
    if (e.relatedTarget && e.relatedTarget.closest('.remove') && e.relatedTarget.closest('.item') === e.target.closest('.item')) return;
    setText(e.target.closest('.item').dataset.id, e.target.value);
  });

  el.title.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); el.title.blur(); }
    if (e.key === 'Escape') { el.title.value = (currentList() || {}).name || ''; el.title.blur(); }
  });
  el.title.addEventListener('blur', () => {
    el.listHead.classList.remove('renaming');
    const l = currentList();
    if (!l) return;
    const name = el.title.value.trim();
    if (!name) { el.title.value = l.name; return; }
    if (name !== l.name) patchList({ name });
  });

  el.share.addEventListener('change', () => {
    const l = currentList();
    if (l && isMine(l)) patchList({ shared: el.share.checked });
  });
  document.querySelectorAll('input[name="color"]').forEach((r) => r.addEventListener('change', () => {
    const l = currentList();
    if (l && isMine(l) && r.checked) patchList({ color: r.value });
  }));

  // Courses récurrentes : tout repasse à faire, chaque élément reprend sa place.
  // Si rien n'est coché, le même bouton coche tout.
  el.uncheck.addEventListener('click', () => {
    const l = currentList();
    if (!l) return;
    const checked = !l.items.some((it) => it.checked);
    const items = l.items.filter((it) => !it.category && it.checked !== checked);
    const ids = items.map((it) => it.id);
    items.forEach((it) => { it.checked = checked; });
    renderItems(l);
    ids.forEach((id) => send('PATCH', '/api/items/' + id, { checked }, [id]));
  });

  el.clear.addEventListener('click', () => {
    const l = currentList();
    if (!l) return;
    const gone = l.items.filter((it) => it.checked);
    const ids = gone.map((it) => it.id);
    if (!ids.length) return;
    remember({ label: itemsLabel(gone), listId: l.id, items: gone.map((it) => ({ ...it })) });
    l.items = l.items.filter((it) => !it.checked);
    renderItems(l);
    // Le serveur ne supprime que ces éléments (s'ils sont encore cochés), par paquets de 200.
    for (let i = 0; i < ids.length; i += 200) {
      const chunk = ids.slice(i, i + 200);
      send('POST', '/api/lists/' + l.id + '/clear-checked', { ids: chunk }, chunk);
    }
  });

  // Suppression : confirmation dans la page.
  el.del.addEventListener('click', () => {
    const l = currentList();
    if (!l) return;
    el.confirmText.textContent = t('confirmDelete', { q: quote(l.name) });
    el.confirm.hidden = false;
    el.del.hidden = true;
    el.delNo.focus();
  });
  el.delNo.addEventListener('click', hideConfirm);
  el.delYes.addEventListener('click', () => {
    const l = currentList();
    if (!l) return;
    remember({ label: t('listDeleted', { q: quote(l.name) }), list: { ...l, items: l.items.map((it) => ({ ...it })) } });
    lists = lists.filter((x) => x.id !== l.id);
    send('DELETE', '/api/lists/' + l.id, undefined, [l.id]);
    open(lists.length ? lists[0].id : null);
  });

  el.undoBtn.addEventListener('click', undoLast);
  // Ctrl+Z / ⌘Z : dans un champ de saisie, il annule la frappe, sauf dans « Ajouter… » vide.
  document.addEventListener('keydown', (e) => {
    if (!(e.ctrlKey || e.metaKey) || e.shiftKey || e.altKey || e.key.toLowerCase() !== 'z' || !undos.length) return;
    const t = e.target;
    if (t.matches('textarea, input:not([type=checkbox]):not([type=radio])') && !(t === el.newItem && t.value === '')) return;
    e.preventDefault();
    undoLast();
  });

  setInterval(() => {
    if (Date.now() - lastEvent > 45000) { setOffline(true); connect(); }
  }, 5000);

  window.addEventListener('online', flush);
  window.addEventListener('offline', () => setOffline(true));
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') { saveState(); return; }
    flush();
    if (!es || es.readyState === EventSource.CLOSED) connect();
  });

  // Déconnexion : rien ne reste sur l'appareil (le service worker oublie la page).
  document.querySelector('form[action="/logout"]').addEventListener('submit', () => {
    clearTimeout(saveTimer);
    loaded = false;
    store.remove(STATE_KEY);
  });

  if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});

  // Ouverture : la copie locale tout de suite (sans réseau, c'est tout ce qu'on a),
  // puis l'état du serveur par-dessus dès qu'il répond.
  current = store.get(LAST_KEY);
  try {
    const saved = JSON.parse(store.get(STATE_KEY) || 'null');
    if (Array.isArray(saved)) {
      lists = saved;
      loaded = true;
      if (!byId(current)) current = pickInitial();
      render();
    }
  } catch { /* copie illisible : on attend le serveur */ }
  resync();
  flush();
  connect();
})();
