package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"
)

// pathID : identifiant de l'URL ; un format invalide est un simple 404.
func pathID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if !validID(id) {
		return "", errNotFound
	}
	return id, nil
}

type stateResponse struct {
	Me    meResponse `json:"me"`
	Lists []*List    `json:"lists"`
}

type meResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (a *App) apiState(w http.ResponseWriter, r *http.Request, s *session) {
	lists, err := visibleLists(a.db, s.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	if lists == nil {
		lists = []*List{}
	}
	writeJSON(w, http.StatusOK, stateResponse{Me: meResponse{s.UserID, s.UserName}, Lists: lists})
}

func (a *App) apiCreateList(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Nonce string `json:"nonce"`
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var l *List
	err := a.write(func(tx *sql.Tx) ([]event, error) {
		var created bool
		var err error
		l, created, err = createList(tx, s.UserID, in.Nonce, in.Name, in.Color)
		if err != nil || !created {
			return nil, err
		}
		return []event{newEvent("list", l, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a *App) apiUpdateList(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var p listPatch
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, err)
		return
	}
	var after *List
	err = a.write(func(tx *sql.Tx) ([]event, error) {
		before, aft, err := updateList(tx, s.UserID, id, p)
		if err != nil {
			return nil, err
		}
		after = aft
		var items []Item
		if !before.Shared && after.Shared {
			if items, err = loadItems(tx, id); err != nil {
				return nil, err
			}
		}
		return listChangedEvents(before, after, items), nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, after)
}

func (a *App) apiDeleteList(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = a.write(func(tx *sql.Tx) ([]event, error) {
		l, err := deleteList(tx, s.UserID, id)
		if err != nil {
			return nil, err
		}
		return []event{newEvent("list-removed", removal{ID: l.ID, Reason: "deleted"}, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) apiCreateItem(w http.ResponseWriter, r *http.Request, s *session) {
	listID, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Nonce string `json:"nonce"`
		Text  string `json:"text"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var it *Item
	err = a.write(func(tx *sql.Tx) ([]event, error) {
		var l *List
		var created bool
		var err error
		it, l, created, err = createItem(tx, s.UserID, listID, in.Nonce, in.Text)
		if err != nil || !created {
			return nil, err
		}
		return []event{newEvent("item", it, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

type itemRemoval struct {
	ID     string `json:"id"`
	ListID string `json:"list_id"`
}

func (a *App) apiUpdateItem(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var p itemPatch
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, err)
		return
	}
	var it *Item
	var deleted bool
	err = a.write(func(tx *sql.Tx) ([]event, error) {
		var l *List
		var err error
		it, l, deleted, err = updateItem(tx, s.UserID, id, p)
		if err != nil {
			return nil, err
		}
		if deleted {
			return []event{newEvent("item-removed", itemRemoval{it.ID, it.ListID}, toViewersOf(l))}, nil
		}
		return []event{newEvent("item", it, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if deleted {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (a *App) apiDeleteItem(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	err = a.write(func(tx *sql.Tx) ([]event, error) {
		it, l, err := deleteItem(tx, s.UserID, id)
		if err != nil {
			return nil, err
		}
		return []event{newEvent("item-removed", itemRemoval{it.ID, it.ListID}, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiMoveItem : {"before": "<id>"} place l'élément avant un autre de la même liste,
// {"before": ""} en dernier.
func (a *App) apiMoveItem(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Before string `json:"before"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Before != "" && !validID(in.Before) {
		writeErr(w, invalidError("position invalide"))
		return
	}
	var moved []itemPosition
	err = a.write(func(tx *sql.Tx) ([]event, error) {
		l, changed, err := moveItem(tx, s.UserID, id, in.Before)
		if err != nil {
			return nil, err
		}
		moved = changed
		if len(changed) == 0 {
			return nil, nil
		}
		return []event{newEvent("items-moved", map[string]any{"list_id": l.ID, "items": changed}, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	if moved == nil {
		moved = []itemPosition{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": moved})
}

func (a *App) apiClearChecked(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var ids []string
	err = a.write(func(tx *sql.Tx) ([]event, error) {
		l, removed, err := clearChecked(tx, s.UserID, id)
		if err != nil {
			return nil, err
		}
		ids = removed
		if len(ids) == 0 {
			return nil, nil
		}
		return []event{newEvent("items-removed", map[string]any{"list_id": l.ID, "ids": ids}, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids})
}

// apiEvents : flux SSE de l'utilisateur. Ne reçoit que les événements dont le
// destinataire passe visibleTo (voir events.go). La session est revérifiée à chaque
// battement : une session révoquée ou expirée coupe le flux.
func (a *App) apiEvents(w http.ResponseWriter, r *http.Request, s *session) {
	sub := a.hub.subscribe(s.UserID, s.Hash)
	if sub == nil {
		writeJSON(w, http.StatusTooManyRequests, apiError{"Trop de connexions ouvertes."})
		return
	}
	defer a.hub.unsubscribe(sub)

	// Pas de WriteTimeout ni de ReadTimeout pour cette seule connexion, qui dure.
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		writeErr(w, err)
		return
	}
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		writeErr(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "retry: 3000\n: ok\n\n")
	if rc.Flush() != nil {
		return
	}

	tick := time.NewTicker(a.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-sub.done:
			return
		case ev := <-sub.ch:
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, ev.data)
		case <-tick.C:
			if !sessionAlive(a.db, s.Hash) {
				return
			}
			fmt.Fprint(w, "event: ping\ndata: \n\n") // signe de vie, surveillé par le client
		}
		if rc.Flush() != nil {
			return
		}
	}
}
