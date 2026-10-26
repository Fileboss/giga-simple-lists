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
	return event{name: name, data: b, to: to}
}

func toViewersOf(l *List) func(string) bool {
	cp := *l
	return func(uid string) bool { return visibleTo(&cp, uid) }
}

// Événements d'une liste qui change. Le passage en privé n'envoie aux autres qu'un
// retrait, sans contenu ; le passage en partagé leur envoie la liste complète.
func listChangedEvents(before, after *List, items []Item) []event {
	meta := *after
	meta.Items = nil
	switch {
	case before.Shared && !after.Shared:
		owner := after.OwnerID
		return []event{
			newEvent(evList, meta, toViewersOf(after)),
			newEvent(evListRemoved, removal{ID: after.ID, Reason: reasonUnshared}, func(uid string) bool { return uid != owner }),
		}
	case !before.Shared && after.Shared:
		full := meta
		full.Items = items
		owner := after.OwnerID
		return []event{
			newEvent(evList, meta, func(uid string) bool { return uid == owner }),
			newEvent(evList, full, func(uid string) bool { return uid != owner && visibleTo(after, uid) }),
		}
	default:
		return []event{newEvent(evList, meta, toViewersOf(after))}
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
)

func newHub() *hub { return &hub{subs: map[*subscriber]struct{}{}} }

// subscribe : nil si la session (ou le serveur) a déjà trop de flux ouverts.
func (h *hub) subscribe(userID string, sessionHash []byte) *subscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for s := range h.subs {
		if s.session == string(sessionHash) {
			n++
		}
	}
	if n >= maxStreamsPerSession || len(h.subs) >= maxStreams {
		return nil
	}
	s := &subscriber{userID: userID, session: string(sessionHash), ch: make(chan event, 64), done: make(chan struct{})}
	h.subs[s] = struct{}{}
	return s
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
			if !ev.to(s.userID) {
				continue
			}
			select {
			case s.ch <- ev:
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
