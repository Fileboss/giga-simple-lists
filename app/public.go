package main

// Lien public d'une liste (/s/<jeton>) : n'importe qui avec le lien voit la liste et coche
// ou décoche ses éléments, sans compte. Rien d'autre : ni ajout, ni texte modifié, ni
// suppression, ni réordonnancement, ni accès à une autre liste. Le propriétaire crée,
// remplace ou révoque le lien ; le remplacer coupe aussitôt les visiteurs de l'ancien.

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"sync"
	"time"
)

func publicURL(origin, token string) string { return origin + "/s/" + token }

// Jeton de randomToken : 256 bits en base64url sans remplissage.
var publicTokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func listByToken(q querier, token string) (*List, error) {
	if !publicTokenRe.MatchString(token) {
		return nil, errNotFound
	}
	l, err := scanList(q.QueryRow(listSelect+` WHERE l.public_token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return l, err
}

// ---------------------------------------------------------------------------
// Propriétaire : lire, créer (ou remplacer) et révoquer le lien.

type publicLinkResponse struct {
	URL string `json:"url"` // vide : pas de lien
}

func (a *App) apiGetPublicLink(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	l, err := accessList(a.db, s.UserID, id)
	if err == nil {
		err = requireOwner(l, s.UserID)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	var token sql.NullString
	if err := a.db.QueryRow(`SELECT public_token FROM lists WHERE id = ?`, id).Scan(&token); err != nil {
		writeErr(w, err)
		return
	}
	res := publicLinkResponse{}
	if token.Valid {
		res.URL = publicURL(a.cfg.Origin, token.String)
	}
	writeJSON(w, http.StatusOK, res)
}

// setPublicToken : nouveau jeton (token non vide) ou révocation (vide).
func (a *App) setPublicToken(w http.ResponseWriter, r *http.Request, s *session, token string) bool {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return false
	}
	err = a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		before, err := accessList(tx, s.UserID, id)
		if err != nil {
			return nil, err
		}
		if err := requireOwner(before, s.UserID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE lists SET public_token = ? WHERE id = ?`, nullable(token), id); err != nil {
			return nil, err
		}
		after := *before
		after.Public = token != ""
		return listChangedEvents(before, &after, nil), nil
	})
	if err != nil {
		writeErr(w, err)
		return false
	}
	a.hub.closeList(id) // les visiteurs de l'ancien lien sont coupés
	return true
}

func (a *App) apiCreatePublicLink(w http.ResponseWriter, r *http.Request, s *session) {
	token := randomToken()
	if a.setPublicToken(w, r, s, token) {
		writeJSON(w, http.StatusOK, publicLinkResponse{URL: publicURL(a.cfg.Origin, token)})
	}
}

func (a *App) apiDeletePublicLink(w http.ResponseWriter, r *http.Request, s *session) {
	if a.setPublicToken(w, r, s, "") {
		w.WriteHeader(http.StatusNoContent)
	}
}

// apiAccounts : les autres comptes de l'instance, pour choisir avec qui partager.
func (a *App) apiAccounts(w http.ResponseWriter, r *http.Request, s *session) {
	rows, err := a.db.Query(`SELECT id, name FROM users WHERE id <> ? ORDER BY name COLLATE NOCASE`, s.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []member{}
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.ID, &m.Name); err != nil {
			writeErr(w, err)
			return
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Visiteurs anonymes

// publicHeaders : le jeton est dans l'URL ; la page ne doit ni le transmettre à un autre
// site, ni être indexée.
func publicHeaders(w http.ResponseWriter) {
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
}

type publicPageData struct {
	Title    string
	Token    string
	List     *List
	Messages map[string]string
}

func (a *App) publicPage(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	l, err := listByToken(a.db, r.PathValue("token"))
	if err != nil {
		if !errors.Is(err, errNotFound) {
			writeErr(w, err)
			return
		}
		a.render(w, http.StatusNotFound, "public.html", publicPageData{Title: T("public.goneTitle")})
		return
	}
	a.render(w, http.StatusOK, "public.html", publicPageData{Title: l.Name, Token: r.PathValue("token"), List: l, Messages: jsMessages()})
}

func (a *App) publicState(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	l, err := listByToken(a.db, r.PathValue("token"))
	if err != nil {
		writeErr(w, err)
		return
	}
	items, err := loadItems(a.db, l.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicList{Name: l.Name, Color: l.Color, Owner: l.OwnerName, Items: items})
}

// Écritures anonymes : au plus publicWritesPerMinute par IP.
const publicWritesPerMinute = 120

var publicWrites = rateLimiter{counts: map[string]int{}}

// publicCheck : {"checked": true|false} sur un élément (pas une catégorie) de la liste du
// lien. Pas de session, donc pas de jeton CSRF : l'Origin et Sec-Fetch-Site sont exigés,
// le corps JSON impose une requête préalable CORS hors du site, et le jeton du lien,
// dans l'URL, est la seule autorisation.
func (a *App) publicCheck(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	if !a.sameOrigin(r) {
		writeJSON(w, http.StatusForbidden, apiError{T("err.refused")})
		return
	}
	if !publicWrites.allow(clientIP(r, a.cfg.TrustedProxies), publicWritesPerMinute) {
		writeJSON(w, http.StatusTooManyRequests, apiError{T("err.tooManyRequests")})
		return
	}
	itemID, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Checked *bool `json:"checked"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	if in.Checked == nil {
		writeErr(w, invalidError("err.badJSON"))
		return
	}
	var it *Item
	err = a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		l, err := listByToken(tx, r.PathValue("token"))
		if err != nil {
			return nil, err
		}
		if err := attachMembers(tx, l); err != nil {
			return nil, err
		}
		if it, err = loadItem(tx, itemID); err != nil {
			return nil, err
		}
		if it.ListID != l.ID {
			return nil, errNotFound
		}
		if it.Category {
			return nil, invalidError("err.categoryCheck")
		}
		it.Checked, it.UpdatedAt = *in.Checked, nowMs()
		if _, err := tx.Exec(`UPDATE items SET checked = ?, updated_at = ?, updated_by = NULL WHERE id = ?`,
			it.Checked, it.UpdatedAt, it.ID); err != nil {
			return nil, err
		}
		return []event{newEvent(evItem, it, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// publicEvents : flux SSE de la liste du lien. Le lien est revérifié à chaque battement :
// révoqué ou remplacé, le flux se coupe (closeList le coupe aussi tout de suite).
func (a *App) publicEvents(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	token := r.PathValue("token")
	l, err := listByToken(a.db, token)
	if err != nil {
		writeErr(w, err)
		return
	}
	sub := a.hub.subscribePublic(l.ID, clientIP(r, a.cfg.TrustedProxies))
	if sub == nil {
		writeJSON(w, http.StatusTooManyRequests, apiError{T("err.tooManyStreams")})
		return
	}
	defer a.hub.unsubscribe(sub)
	a.stream(w, r, sub, func() bool {
		cur, err := listByToken(a.db, token)
		return err == nil && cur.ID == l.ID
	})
}

// ---------------------------------------------------------------------------

// rateLimiter : compteur par clé sur une fenêtre fixe d'une minute, en mémoire.
type rateLimiter struct {
	mu     sync.Mutex
	window time.Time
	counts map[string]int
}

func (rl *rateLimiter) allow(key string, max int) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	t := now()
	if t.Sub(rl.window) >= time.Minute || len(rl.counts) > 10000 {
		rl.window, rl.counts = t, map[string]int{}
	}
	if rl.counts[key] >= max {
		return false
	}
	rl.counts[key]++
	return true
}
