package main

// Ouverture vers le reste du homelab, sans charger l'interface :
//   - dupliquer une liste (un modèle « Valise vacances » réutilisé) ;
//   - exporter ses listes en JSON, et en importer (toujours de nouvelles listes) ;
//   - jeton d'API personnel et une route pour ajouter un élément (Home Assistant, Siri) ;
//   - authentification par en-tête d'un reverse proxy (Authelia, Authentik), en option.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Dupliquer une liste

// duplicateList : copie privée, à soi, d'une liste visible ; éléments décochés (une
// liste « modèle » se réutilise vide). Le nonce rend la requête idempotente.
func duplicateList(tx *sql.Tx, userID, listID, nonce string) (*List, bool, error) {
	src, err := accessList(tx, userID, listID)
	if err != nil {
		return nil, false, err
	}
	items, err := loadItems(tx, src.ID)
	if err != nil {
		return nil, false, err
	}
	name := []rune(tf("list.copyName", src.Name))
	if len(name) > maxListName {
		name = name[:maxListName]
	}
	l, created, err := createList(tx, userID, nonce, string(name), src.Color, false, 0)
	if err != nil || !created {
		return l, false, err
	}
	t := nowMs()
	for i, it := range items {
		x := Item{ID: newID(), ListID: l.ID, Text: it.Text, Category: it.Category, Position: int64(i + 1), UpdatedAt: t}
		if _, err := tx.Exec(`INSERT INTO items (id, list_id, text, checked, category, position, updated_at, updated_by)
			VALUES (?, ?, ?, 0, ?, ?, ?, ?)`, x.ID, x.ListID, x.Text, x.Category, x.Position, x.UpdatedAt, userID); err != nil {
			return nil, false, err
		}
		l.Items = append(l.Items, x)
	}
	return l, true, nil
}

func (a *App) apiDuplicateList(w http.ResponseWriter, r *http.Request, s *session) {
	id, err := pathID(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var in struct {
		Nonce string `json:"nonce"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var l *List
	err = a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		var created bool
		var err error
		if l, created, err = duplicateList(tx, s.UserID, id, in.Nonce); err != nil || !created {
			return nil, err
		}
		return []event{newEvent(evList, l, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// ---------------------------------------------------------------------------
// Export et import JSON

const (
	exportFormat   = "giga-simple-lists"
	exportVersion  = 1
	maxImportBytes = 2 << 20
)

type exportFile struct {
	Format     string       `json:"format"`
	Version    int          `json:"version"`
	ExportedAt string       `json:"exported_at"`
	Lists      []exportList `json:"lists"`
}

type exportList struct {
	Name  string       `json:"name"`
	Color string       `json:"color"`
	Items []exportItem `json:"items"`
}

type exportItem struct {
	Text     string `json:"text"`
	Checked  bool   `json:"checked,omitempty"`
	Category bool   `json:"category,omitempty"`
}

// accountExport : les listes dont le compte est propriétaire (pas celles des autres),
// avec leurs éléments, dans l'ordre affiché.
func (a *App) accountExport(w http.ResponseWriter, r *http.Request, s *session) {
	lists, err := visibleLists(a.db, s.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := exportFile{Format: exportFormat, Version: exportVersion, ExportedAt: now().UTC().Format(time.RFC3339), Lists: []exportList{}}
	for _, l := range lists {
		if l.OwnerID != s.UserID {
			continue
		}
		el := exportList{Name: l.Name, Color: l.Color, Items: []exportItem{}}
		for _, it := range l.Items {
			el.Items = append(el.Items, exportItem{Text: it.Text, Checked: it.Checked, Category: it.Category})
		}
		out.Lists = append(out.Lists, el)
	}
	name := fmt.Sprintf("giga-simple-lists-%s-%s.json", s.UserName, now().Format("2006-01-02"))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(out)
}

// importLists : nouvelles listes privées, jamais de fusion ni d'écrasement. Tout ou rien.
func importLists(tx *sql.Tx, userID string, f exportFile) ([]*List, error) {
	if f.Format != exportFormat || f.Version != exportVersion {
		return nil, invalidError("err.importFormat")
	}
	var out []*List
	for _, in := range f.Lists {
		color := in.Color
		if !validColor(color) {
			color = ""
		}
		l, _, err := createList(tx, userID, "", in.Name, color, false, 0)
		if err != nil {
			return nil, err
		}
		if len(in.Items) > maxItemsPerList {
			return nil, errLimit
		}
		t := nowMs()
		for i, x := range in.Items {
			text, err := cleanText(x.Text, maxItemText)
			if err != nil {
				return nil, err
			}
			if text == "" {
				continue
			}
			it := Item{ID: newID(), ListID: l.ID, Text: text, Checked: x.Checked && !x.Category, Category: x.Category,
				Position: int64(i + 1), UpdatedAt: t}
			if _, err := tx.Exec(`INSERT INTO items (id, list_id, text, checked, category, position, updated_at, updated_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, it.ID, it.ListID, it.Text, it.Checked, it.Category, it.Position, it.UpdatedAt, userID); err != nil {
				return nil, err
			}
			l.Items = append(l.Items, it)
		}
		out = append(out, l)
	}
	return out, nil
}

func (a *App) accountImport(w http.ResponseWriter, r *http.Request, s *session) {
	file, _, err := r.FormFile("file")
	if err != nil {
		a.renderAccount(w, http.StatusBadRequest, s, "", T("err.importFile"))
		return
	}
	defer file.Close()
	var f exportFile
	dec := json.NewDecoder(io.LimitReader(file, maxImportBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		a.renderAccount(w, http.StatusBadRequest, s, "", T("err.importFormat"))
		return
	}
	var lists []*List
	err = a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		var err error
		if lists, err = importLists(tx, s.UserID, f); err != nil {
			return nil, err
		}
		evs := make([]event, 0, len(lists))
		for _, l := range lists {
			evs = append(evs, newEvent(evList, l, toViewersOf(l)))
		}
		return evs, nil
	})
	var inv invalidError
	switch {
	case errors.As(err, &inv):
		a.renderAccount(w, http.StatusBadRequest, s, "", inv.Error())
		return
	case errors.Is(err, errLimit):
		a.renderAccount(w, http.StatusBadRequest, s, "", T("err.limit"))
		return
	case err != nil:
		writeErr(w, err)
		return
	}
	slog.Info(T("log.imported"), "user", s.UserName, "lists", len(lists))
	http.Redirect(w, r, "/account?done=imported", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Jeton d'API personnel

// apiTokenPrefix : reconnaissable dans un fichier de configuration ou un scanner de secrets.
const apiTokenPrefix = "gsl_"

func hasAPIToken(q querier, userID string) (created int64, lastUsed int64, ok bool, err error) {
	err = q.QueryRow(`SELECT created_at, last_used_at FROM api_tokens WHERE user_id = ?`, userID).Scan(&created, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	return created, lastUsed, err == nil, err
}

// newAPIToken remplace le jeton du compte (l'ancien cesse de marcher) et le renvoie : il
// n'est affiché qu'une fois.
func newAPIToken(q querier, userID string) (string, error) {
	token := apiTokenPrefix + randomToken()
	_, err := q.Exec(`INSERT INTO api_tokens (user_id, token_hash, created_at) VALUES (?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET token_hash = excluded.token_hash, created_at = excluded.created_at, last_used_at = 0`,
		userID, tokenHash(token), nowMs())
	return token, err
}

func (a *App) accountNewToken(w http.ResponseWriter, r *http.Request, s *session) {
	token, err := newAPIToken(a.db, s.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	slog.Info(T("log.apiTokenCreated"), "user", s.UserName)
	p := a.accountPageData(w, s)
	if p == nil {
		return
	}
	p.NewToken = token
	a.render(w, http.StatusOK, "account.html", *p)
}

func (a *App) accountRevokeToken(w http.ResponseWriter, r *http.Request, s *session) {
	if _, err := a.db.Exec(`DELETE FROM api_tokens WHERE user_id = ?`, s.UserID); err != nil {
		writeErr(w, err)
		return
	}
	http.Redirect(w, r, "/account?done=token", http.StatusSeeOther)
}

// apiTokenUser : le compte du jeton « Authorization: Bearer gsl_… », ou nil.
func apiTokenUser(q querier, r *http.Request) (*session, error) {
	auth := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(auth, "Bearer ")
	if !ok || !strings.HasPrefix(token, apiTokenPrefix) || len(token) > 100 {
		return nil, errNotFound
	}
	s := &session{}
	err := q.QueryRow(`SELECT u.id, u.name, u.is_admin FROM api_tokens t JOIN users u ON u.id = t.user_id WHERE t.token_hash = ?`,
		tokenHash(token)).Scan(&s.UserID, &s.UserName, &s.IsAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return s, err
}

// Appels avec jeton : au plus apiTokenPerMinute par compte.
const apiTokenPerMinute = 60

// tokenAPI : routes /api/v1, authentifiées par le seul jeton d'API (ni cookie ni CSRF :
// aucun navigateur n'envoie d'en-tête Authorization tout seul).
func (a *App) tokenAPI(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := apiTokenUser(a.db, r)
		if err != nil {
			if !errors.Is(err, errNotFound) {
				errorLog("api_tokens", err)
			}
			w.Header().Set("WWW-Authenticate", `Bearer realm="giga-simple-lists"`)
			writeJSON(w, http.StatusUnauthorized, apiError{T("err.apiToken")})
			return
		}
		if !a.tokenCalls.allow(s.UserID, apiTokenPerMinute) {
			writeJSON(w, http.StatusTooManyRequests, apiError{T("err.tooManyRequests")})
			return
		}
		if _, err := a.db.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE user_id = ?`, nowMs(), s.UserID); err != nil {
			errorLog("api_tokens", err)
		}
		h(w, r, s)
	}
}

type apiListSummary struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Owner string `json:"owner"`
	Items int    `json:"items"` // éléments à faire (non cochés, hors catégories)
}

// apiV1Lists : les listes visibles, pour trouver le nom ou l'identifiant à utiliser.
func (a *App) apiV1Lists(w http.ResponseWriter, r *http.Request, s *session) {
	lists, err := visibleLists(a.db, s.UserID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := []apiListSummary{}
	for _, l := range lists {
		n := 0
		for _, it := range l.Items {
			if !it.Checked && !it.Category {
				n++
			}
		}
		out = append(out, apiListSummary{ID: l.ID, Name: l.Name, Owner: l.OwnerName, Items: n})
	}
	writeJSON(w, http.StatusOK, out)
}

// findList : la liste visible désignée par son identifiant, ou par son nom (sans tenir
// compte de la casse ; à nom égal, ses propres listes d'abord).
func findList(q querier, userID, ref string) (*List, error) {
	ref = strings.TrimSpace(ref)
	if validID(ref) {
		if l, err := accessList(q, userID, ref); err == nil {
			return l, nil
		}
	}
	lists, err := visibleLists(q, userID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(lists, func(l *List) bool { return strings.EqualFold(l.Name, ref) })
	if i < 0 {
		return nil, errNotFound
	}
	return lists[i], nil
}

// apiV1AddItem : {"list": "Courses", "text": "lait"} ajoute un élément à la fin de la
// liste, comme le champ « Ajouter » ; les autres le voient en temps réel.
func (a *App) apiV1AddItem(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		List     string `json:"list"`
		Text     string `json:"text"`
		Category bool   `json:"category"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	var it *Item
	err := a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		l, err := findList(tx, s.UserID, in.List)
		if err != nil {
			return nil, err
		}
		var created bool
		it, l, created, err = createItem(tx, s.UserID, l.ID, "", in.Text, in.Category)
		if err != nil || !created {
			return nil, err
		}
		return []event{newEvent(evItem, it, toViewersOf(l))}, nil
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, it)
}

// ---------------------------------------------------------------------------
// Authentification par en-tête du reverse proxy (LISTES_AUTH_HEADER)

// headerUser : avec LISTES_AUTH_HEADER, le compte nommé par cet en-tête, seulement si la
// connexion vient d'un relais de confiance (LISTES_TRUSTED_PROXIES) : sinon n'importe qui
// pourrait l'écrire lui-même. Le compte doit exister (pas de création automatique).
func (a *App) headerUser(r *http.Request) *user {
	if a.cfg.AuthHeader == "" {
		return nil
	}
	name := strings.TrimSpace(r.Header.Get(a.cfg.AuthHeader))
	if name == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !slices.Contains(a.cfg.TrustedProxies, host) {
		return nil
	}
	u, err := userByNameFold(a.db, name)
	if err != nil {
		if errors.Is(err, errNotFound) {
			throttledLog(T("log.authHeaderUnknown"), "user", name)
		} else {
			errorLog("users", err)
		}
		return nil
	}
	return u
}

// userByNameFold : comme userByName, sans tenir compte de la casse (accents compris).
func userByNameFold(q querier, name string) (*user, error) {
	rows, err := q.Query(`SELECT id, name, password_hash FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var u user
		if err := rows.Scan(&u.ID, &u.Name, &u.PasswordHash); err != nil {
			return nil, err
		}
		if strings.EqualFold(u.Name, name) {
			return &u, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, errNotFound
}

// headerSession ouvre une session pour le compte authentifié par le reverse proxy (et
// détruit celle d'un autre compte que portait le navigateur).
func (a *App) headerSession(w http.ResponseWriter, r *http.Request, u *user) (*session, error) {
	if old, err := r.Cookie(a.cookieName(sessionCookie)); err == nil {
		deleteSession(a.db, tokenHash(old.Value))
	}
	token, s, err := createSession(a.db, u.ID, r.UserAgent())
	if err != nil {
		errorLog(T("log.sessionCreate"), err)
		return nil, err
	}
	s.UserName = u.Name
	if err := a.db.QueryRow(`SELECT is_admin FROM users WHERE id = ?`, u.ID).Scan(&s.IsAdmin); err != nil {
		return nil, err
	}
	slog.Info(T("log.loginHeader"), "ip", clientIP(r, a.cfg.TrustedProxies), "user", u.Name)
	a.setSessionCookie(w, token, s)
	return s, nil
}
