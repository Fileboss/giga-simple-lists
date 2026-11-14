package main

import (
	"encoding/json"
	"sync"
)

// event : un message du flux SSE. `to` décide, pour chaque utilisateur abonné, s'il le
// reçoit ; pour tout ce qui concerne une liste, c'est visibleTo sur l'état de la liste
// après la modification. Les événements sont publiés sous le verrou d'écriture
// (App.write) : leur ordre est celui des commits, et la visibilité évaluée est exacte.
type event struct {
	name string
	data []byte
	to   func(userID string) bool
	// Lien public : la liste concernée, et ce que reçoivent ses visiteurs anonymes (nil :
	// rien). Seuls les éléments, le nom et la couleur leur sont envoyés, jamais les
	// comptes, les membres ou le partage.
	listID string
	public []byte
}

// Noms des événements SSE (écoutés par app.js, vérifié par TestEventNamesInJS) et
// raisons d'un retrait de liste.
const (
	evList         = "list"
	evListRemoved  = "list-removed"
	evItem         = "item"
	evItemRemoved  = "item-removed"
	evItemsRemoved = "items-removed"
	evItemsMoved   = "items-moved"

	reasonDeleted  = "deleted"
	reasonUnshared = "unshared"
)

func newEvent(name string, v any, to func(string) bool) event {
	b, _ := json.Marshal(v)
	ev := event{name: name, data: b, to: to}
	switch x := v.(type) {
	case *Item:
		ev.listID, ev.public = x.ListID, b
	case itemRemoval:
		ev.listID, ev.public = x.ListID, b
	case itemsMoved:
		ev.listID, ev.public = x.ListID, b
	case itemsRemoved:
		ev.listID, ev.public = x.ListID, b
	case removal:
		if x.Reason == reasonDeleted { // la liste n'existe plus : la page publique le dit
			ev.listID, ev.public = x.ID, b
		}
	}
	return ev
}

// publicList : ce qu'un visiteur du lien public voit de la liste.
type publicList struct {
	Name  string `json:"name"`
	Color string `json:"color"`
	Items []Item `json:"items,omitempty"`
}

func publicListEvent(l *List) event {
	b, _ := json.Marshal(publicList{Name: l.Name, Color: l.Color})
	return event{name: evList, to: func(string) bool { return false }, listID: l.ID, public: b}
}

func toViewersOf(l *List) func(string) bool {
	cp := *l
	return func(uid string) bool { return visibleTo(&cp, uid) }
}

// Événements d'une liste qui change, selon ce que chacun voyait avant et voit après :
// qui la gardait reçoit les nouvelles métadonnées, qui la découvre la liste complète
// (items), qui la perd un simple retrait, sans contenu. Les visiteurs du lien public
// reçoivent nom et couleur.
func listChangedEvents(before, after *List, items []Item) []event {
	meta := *after
	meta.Items = nil
	full := meta
	full.Items = items
	b, a := *before, *after
	return []event{
		newEvent(evList, meta, func(uid string) bool { return visibleTo(&b, uid) && visibleTo(&a, uid) }),
		newEvent(evList, full, func(uid string) bool { return !visibleTo(&b, uid) && visibleTo(&a, uid) }),
		newEvent(evListRemoved, removal{ID: after.ID, Reason: reasonUnshared}, func(uid string) bool {
			return visibleTo(&b, uid) && !visibleTo(&a, uid)
		}),
		publicListEvent(after),
	}
}

type removal struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type itemRemoval struct {
	ID     string `json:"id"`
	ListID string `json:"list_id"`
}

type itemsMoved struct {
	ListID string         `json:"list_id"`
	Items  []itemPosition `json:"items"`
}

type itemsRemoved struct {
	ListID string   `json:"list_id"`
	IDs    []string `json:"ids"`
}

// ---------------------------------------------------------------------------

type subscriber struct {
	userID  string
	listID  string // visiteur du lien public de cette liste (userID et session vides)
	ip      string
	session string // hash du jeton de session
	ch      chan event
	done    chan struct{}
	once    sync.Once
}

func (s *subscriber) close() { s.once.Do(func() { close(s.done) }) }

type hub struct {
	mu   sync.Mutex
	subs map[*subscriber]struct{}
}

const (
	maxStreamsPerSession = 4
	maxStreams           = 64
	// Visiteurs anonymes des liens publics : un plafond à part, pour qu'ils ne privent
	// jamais les comptes de leurs flux.
	// Par IP (réseau /64 en IPv6) : une famille derrière une même box ; par lien : quiconque
	// a un lien ne peut saturer que celui-là.
	maxPublicStreams        = 64
	maxPublicStreamsPerIP   = 8
	maxPublicStreamsPerList = 16
)

func newHub() *hub { return &hub{subs: map[*subscriber]struct{}{}} }

// subscribe : nil si la session (ou le serveur) a déjà trop de flux ouverts.
func (h *hub) subscribe(userID string, sessionHash []byte) *subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	total, sameSession := 0, 0
	for s := range h.subs {
		if s.listID == "" {
			total++
		}
		if s.session == string(sessionHash) {
			sameSession++
		}
	}
	if sameSession >= maxStreamsPerSession || total >= maxStreams {
		return nil
	}
	s := &subscriber{userID: userID, session: string(sessionHash), ch: make(chan event, 64), done: make(chan struct{})}
	h.subs[s] = struct{}{}
	return s
}

// subscribePublic : flux d'un visiteur du lien public d'une liste ; nil au-delà des plafonds.
func (h *hub) subscribePublic(listID, ip string) *subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	total, sameIP, sameList := 0, 0, 0
	for s := range h.subs {
		if s.listID != "" {
			total++
			if s.ip == ip {
				sameIP++
			}
			if s.listID == listID {
				sameList++
			}
		}
	}
	if total >= maxPublicStreams || sameIP >= maxPublicStreamsPerIP || sameList >= maxPublicStreamsPerList {
		return nil
	}
	s := &subscriber{listID: listID, ip: ip, ch: make(chan event, 64), done: make(chan struct{})}
	h.subs[s] = struct{}{}
	return s
}

// closeList coupe les flux publics d'une liste (lien révoqué ou remplacé).
func (h *hub) closeList(listID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.listID == listID {
			delete(h.subs, s)
			s.close()
		}
	}
}

func (h *hub) unsubscribe(s *subscriber) {
	h.mu.Lock()
	delete(h.subs, s)
	h.mu.Unlock()
	s.close()
}

// publish ne bloque jamais : un abonné qui ne suit pas est déconnecté (le client se
// reconnecte et recharge l'état).
func (h *hub) publish(evs ...event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ev := range evs {
		for s := range h.subs {
			out := ev
			if s.listID != "" {
				if ev.listID != s.listID || ev.public == nil {
					continue
				}
				out.data = ev.public
			} else if !ev.to(s.userID) {
				continue
			}
			select {
			case s.ch <- out:
			default:
				delete(h.subs, s)
				s.close()
			}
		}
	}
}

// closeSession coupe les flux d'une session (déconnexion).
func (h *hub) closeSession(sessionHash []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if s.session == string(sessionHash) {
			delete(h.subs, s)
			s.close()
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		delete(h.subs, s)
		s.close()
	}
}
