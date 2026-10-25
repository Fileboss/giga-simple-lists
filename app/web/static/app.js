// Listes : rendu, mises à jour optimistes, file d'attente hors ligne, temps réel (SSE).
'use strict';
(() => {
  const ME = document.body.dataset.me;
  const CSRF = document.querySelector('meta[name="csrf-token"]').content;
  const QUEUE_KEY = 'listes.queue.' + ME;
  const LAST_KEY = 'listes.last.' + ME;
  const $ = (id) => document.getElementById(id);
  const el = {
    tabs: $('tabs'), tabsPrev: $('tabs-prev'), tabsNext: $('tabs-next'), newList: $('new-list'), offline: $('offline'), toast: $('toast'),
    list: $('list'), title: $('title'), share: $('share'), ownerNote: $('owner-note'),
    add: $('add'), newItem: $('new-item'), addBtn: $('add-btn'), items: $('items'), clear: $('clear-checked'),
    ownerTools: $('owner-tools'), del: $('delete'), confirm: $('confirm-delete'),
    confirmText: $('confirm-text'), delYes: $('delete-yes'), delNo: $('delete-no'), empty: $('empty'),
  };
  const COLORS = [...document.querySelectorAll('input[name="color"]')].map((i) => i.value);

  let lists = [];          // [{id, owner_id, owner_name, name, color, shared, position, items: [...]}]
  let current = null;      // id de la liste affichée
  let loaded = false;

  // ---------------------------------------------------------------------------
  // Utilitaires

  const store = {
    get(k) { try { return localStorage.getItem(k); } catch { return null; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch { /* stockage indisponible */ } },
  };

  function randomHex() {
    const b = new Uint8Array(16);
    crypto.getRandomValues(b);
    return [...b].map((x) => x.toString(16).padStart(2, '0')).join('');
  }

  // Même calcul que creationID côté serveur : l'identifiant est connu avant la réponse.
  async function creationID(nonce) {
    const data = new TextEncoder().encode('listes-id\0' + ME + '\0' + nonce);
    const h = new Uint8Array(await crypto.subtle.digest('SHA-256', data));
    return [...h.slice(0, 16)].map((x) => x.toString(16).padStart(2, '0')).join('');
  }

  const byId = (id) => lists.find((l) => l.id === id);
  const currentList = () => byId(current);
  const isMine = (l) => l.owner_id === ME;

  let toastTimer;
  function toast(msg) {
    el.toast.textContent = msg;
    el.toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { el.toast.hidden = true; }, 4000);
  }

  // ---------------------------------------------------------------------------
  // File d'attente : chaque modification part dans l'ordre ; en cas de coupure, elle
  // reste dans la file (sauvegardée localement) et repart au retour du réseau.

  let queue = [];
  try { queue = JSON.parse(store.get(QUEUE_KEY) || '[]'); } catch { queue = []; }
  const pending = new Map(); // id d'élément ou de liste -> opérations en attente
  const touch = (ids, d) => ids.forEach((id) => {
    const n = (pending.get(id) || 0) + d;
    if (n > 0) pending.set(id, n); else pending.delete(id);
  });
  queue.forEach((op) => touch(op.ids, 1));
  const saveQueue = () => store.set(QUEUE_KEY, JSON.stringify(queue));

  function send(method, url, body, ids) {
    queue.push({ method, url, body, ids });
    touch(ids, 1);
    saveQueue();
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
        touch(op.ids, -1);
        saveQueue();
        if (!res.ok) {
          // Refus définitif (404 : la liste n'est plus visible, 403, 400…) : on abandonne
          // l'opération et on recharge l'état du serveur.
          if (res.status !== 404) {
            const err = await res.json().catch(() => ({}));
            toast(err.error || 'Modification refusée.');
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
      toast(reason === 'unshared' ? 'Cette liste n’est plus partagée' : 'Cette liste a été supprimée');
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
    renderTabs();
    const l = currentList();
    el.list.hidden = !l;
    el.empty.hidden = !!l || !loaded;
    if (!l) return;
    el.list.className = 'list c-' + l.color;
    if (document.activeElement !== el.title) el.title.value = l.name;
    const mine = isMine(l);
    el.share.checked = l.shared;
    el.share.disabled = !mine;
    el.ownerTools.hidden = !mine;
    el.ownerNote.hidden = mine;
    el.ownerNote.textContent = mine ? '' : 'Liste de ' + l.owner_name + ', partagée avec toi.';
    document.querySelectorAll('input[name="color"]').forEach((r) => { r.checked = r.value === l.color; });
    renderItems(l);
  }

  function renderTabs() {
    const sorted = [...lists].sort((a, b) => (isMine(b) - isMine(a)) || (a.position - b.position));
    el.tabs.replaceChildren(...sorted.map((l) => {
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
        from.textContent = ' · de ' + l.owner_name;
        b.append(from);
      }
      if (l.shared) {
        const mark = document.createElement('span');
        mark.className = 'mark';
        mark.textContent = '⇄';
        mark.title = 'Partagée';
        mark.setAttribute('aria-label', 'partagée');
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

  // Cochés en bas, en gris. Les lignes existantes sont réutilisées pour ne pas
  // perdre le focus d'un texte en cours de modification.
  function renderItems(l) {
    if (drag) { drag.renderLater = true; return; }
    const ordered = [...l.items].sort((a, b) => (a.checked - b.checked) || (a.position - b.position));
    const existing = new Map([...el.items.children].map((li) => [li.dataset.id, li]));
    let ref = el.items.firstChild;
    for (const it of ordered) {
      const li = existing.get(it.id) || makeRow(it.id);
      existing.delete(it.id);
      li.classList.toggle('done', it.checked);
      li.querySelector('.check input').checked = it.checked;
      const text = li.querySelector('.text');
      if (document.activeElement !== text && text.value !== it.text) text.value = it.text;
      fitText(text);
      if (li !== ref) el.items.insertBefore(li, ref); else ref = ref.nextSibling;
    }
    existing.forEach((li) => li.remove());
    el.clear.hidden = !l.items.some((it) => it.checked);
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
    handle.setAttribute('aria-label', 'Déplacer (flèches haut et bas)');
    const label = document.createElement('label');
    label.className = 'check';
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.setAttribute('aria-label', 'Cocher');
    label.append(box);
    // Zone de texte qui passe à la ligne (jusqu'à 3 lignes), sans retour chariot possible.
    const text = document.createElement('textarea');
    text.className = 'text';
    text.rows = 1;
    text.maxLength = 500;
    text.autocomplete = 'off';
    text.enterKeyHint = 'done';
    text.setAttribute('aria-label', 'Texte de l’élément');
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'remove';
    remove.textContent = '−';
    remove.setAttribute('aria-label', 'Retirer cet élément');
    li.append(handle, label, text, remove);
    return li;
  }

  // ---------------------------------------------------------------------------
  // Actions

  async function addItem(text) {
    const l = currentList();
    if (!l) return;
    const nonce = randomHex();
    const id = await creationID(nonce);
    const pos = l.items.reduce((m, it) => Math.max(m, it.position), 0) + 1;
    l.items.push({ id, list_id: l.id, text, checked: false, position: pos, updated_at: Date.now() });
    renderItems(l);
    send('POST', '/api/lists/' + l.id + '/items', { nonce, text }, [id]);
  }

  function findItem(id) {
    const l = currentList();
    return l && l.items.find((x) => x.id === id);
  }

  function setChecked(id, checked) {
    const it = findItem(id);
    if (!it || it.checked === checked) return;
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
      l.items = l.items.filter((x) => x.id !== id);
      send('DELETE', '/api/items/' + id, undefined, [id]);
    } else {
      it.text = text;
      send('PATCH', '/api/items/' + id, { text }, [id]);
    }
    renderItems(l);
  }

  // Même calcul que moveItem côté serveur : l'élément passe juste avant `before`
  // (ou en dernier), puis les positions sont renumérotées.
  function moveItem(id, before) {
    const l = currentList();
    if (!l) return;
    const order = [...l.items].sort((a, b) => a.position - b.position);
    const it = order.find((x) => x.id === id);
    if (!it) return;
    const rest = order.filter((x) => x !== it);
    const at = before ? rest.findIndex((x) => x.id === before) : rest.length;
    if (at < 0) return;
    rest.splice(at, 0, it);
    rest.forEach((x, i) => { x.position = i + 1; });
    send('POST', '/api/items/' + id + '/move', { before: before || '' }, [id]);
  }

  // Voisins du même groupe (cochés ou non) que la ligne li, dans l'ordre affiché.
  const groupOf = (li) => [...el.items.children].filter((x) => x.classList.contains('done') === li.classList.contains('done'));

  // Après un déplacement dans le DOM : l'élément suivant du groupe donne la cible.
  function commitRowMove(li, before0) {
    const group = groupOf(li);
    const next = group[group.indexOf(li) + 1];
    const before = next ? next.dataset.id : '';
    if (before === before0) return false;
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
    const group = groupOf(li);
    const next = group[group.indexOf(li) + 1];
    const top = li.getBoundingClientRect().top;
    drag = { li, y: e.clientY, grab: e.clientY - top, dy: 0, before0: next ? next.dataset.id : '', renderLater: false };
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
    const same = (x) => x && x.classList.contains('done') === li.classList.contains('done');
    const mid = (x) => x.offsetTop + x.offsetHeight / 2;
    const top = drag.y - el.items.getBoundingClientRect().top - drag.grab; // haut voulu, repère de la liste
    const center = top + li.offsetHeight / 2;
    const prev = li.previousElementSibling;
    const next = li.nextElementSibling;
    if (same(prev) && center < mid(prev)) flip([prev], () => li.after(prev));
    else if (same(next) && center > mid(next)) flip([next], () => li.before(next));
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
    const { li, before0, renderLater, dy, slot } = drag;
    drag = null;
    if (reduceMotion) slot.remove();
    else slot.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 180, easing: EASE }).finished.then(() => slot.remove());
    li.style.transform = '';
    li.classList.remove('dragging'); // l'ombre s'estompe (transition CSS)
    document.body.classList.remove('is-dragging');
    if (!reduceMotion) {
      li.animate([{ transform: 'translateY(' + dy + 'px)' + LIFT }, { transform: 'none' }], { duration: 180, easing: EASE });
    }
    if (!(commit && commitRowMove(li, before0)) || renderLater) renderItems(currentList());
  }

  function removeItem(id) {
    const l = currentList();
    if (!l || !l.items.some((x) => x.id === id)) return;
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
    lists.push({ id, owner_id: ME, owner_name: document.body.dataset.name, name: 'Nouvelle liste',
      color, shared: false, position, items: [] });
    send('POST', '/api/lists', { nonce, name: 'Nouvelle liste', color }, [id]);
    open(id);
    el.title.focus();
    el.title.select();
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
    addItem(text);
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
    const group = groupOf(li);
    const i = group.indexOf(li);
    const before0 = group[i + 1] ? group[i + 1].dataset.id : '';
    if (e.key === 'ArrowUp' && i > 0) flip([li, group[i - 1]], () => el.items.insertBefore(li, group[i - 1]));
    else if (e.key === 'ArrowDown' && i < group.length - 1) flip([li, group[i + 1]], () => group[i + 1].after(li));
    else return;
    commitRowMove(li, before0);
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

  el.clear.addEventListener('click', () => {
    const l = currentList();
    if (!l) return;
    const ids = l.items.filter((it) => it.checked).map((it) => it.id);
    if (!ids.length) return;
    l.items = l.items.filter((it) => !it.checked);
    renderItems(l);
    send('POST', '/api/lists/' + l.id + '/clear-checked', {}, ids);
  });

  // Suppression : confirmation dans la page.
  el.del.addEventListener('click', () => {
    const l = currentList();
    if (!l) return;
    el.confirmText.textContent = 'Supprimer « ' + l.name + ' » et tous ses éléments ?';
    el.confirm.hidden = false;
    el.del.hidden = true;
    el.delNo.focus();
  });
  el.delNo.addEventListener('click', hideConfirm);
  el.delYes.addEventListener('click', () => {
    const l = currentList();
    if (!l) return;
    lists = lists.filter((x) => x.id !== l.id);
    send('DELETE', '/api/lists/' + l.id, undefined, [l.id]);
    open(lists.length ? lists[0].id : null);
  });

  setInterval(() => {
    if (Date.now() - lastEvent > 45000) { setOffline(true); connect(); }
  }, 5000);

  window.addEventListener('online', flush);
  window.addEventListener('offline', () => setOffline(true));
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState !== 'visible') return;
    flush();
    if (!es || es.readyState === EventSource.CLOSED) connect();
  });

  current = store.get(LAST_KEY);
  resync();
  flush();
  connect();
})();
