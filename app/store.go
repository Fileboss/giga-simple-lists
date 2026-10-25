package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limites (aussi vérifiées par les tests).
const (
	maxItemText      = 500
	maxListName      = 80
	maxListsPerUser  = 200
	maxItemsPerList  = 2000
	defaultListName  = "Nouvelle liste"
	maxBodyBytes     = 16 << 10
	maxUserNameRunes = 32
)

var (
	errNotFound  = errors.New("introuvable")
	errForbidden = errors.New("réservé au propriétaire de la liste")
	errLimit     = errors.New("limite atteinte")
)

type invalidError string

func (e invalidError) Error() string { return string(e) }

type List struct {
	ID        string `json:"id"`
	OwnerID   string `json:"owner_id"`
	OwnerName string `json:"owner_name"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Shared    bool   `json:"shared"`
	Position  int64  `json:"position"`
	Items     []Item `json:"items,omitempty"`
}

type Item struct {
	ID        string `json:"id"`
	ListID    string `json:"list_id"`
	Text      string `json:"text"`
	Checked   bool   `json:"checked"`
	Position  int64  `json:"position"`
	UpdatedAt int64  `json:"updated_at"`
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
	Exec(query string, args ...any) (sql.Result, error)
}

// ---------------------------------------------------------------------------
// Contrôle d'accès : toute la règle de visibilité tient dans visibleTo, et tout
// accès à une liste ou à un élément passe par accessList / accessItem.
// Le flux SSE filtre ses destinataires avec visibleTo (voir events.go).

// visibleTo : une liste est visible (et modifiable) par son propriétaire, ou par
// tous les utilisateurs si elle est partagée.
func visibleTo(l *List, userID string) bool {
	return l.OwnerID == userID || l.Shared
}

// accessList renvoie la liste si userID peut la voir. Absente ou invisible :
// errNotFound dans les deux cas (404), pour ne pas révéler qu'elle existe.
func accessList(q querier, userID, listID string) (*List, error) {
	l, err := loadList(q, listID)
	if err != nil {
		return nil, err
	}
	if !visibleTo(l, userID) {
		return nil, errNotFound
	}
	return l, nil
}

// accessItem retrouve la liste de l'élément côté serveur (jamais d'après le client)
// et applique accessList.
func accessItem(q querier, userID, itemID string) (*Item, *List, error) {
	it, err := loadItem(q, itemID)
	if err != nil {
		return nil, nil, err
	}
	l, err := accessList(q, userID, it.ListID)
	if err != nil {
		return nil, nil, err
	}
	return it, l, nil
}

// requireOwner : partage, couleur et suppression sont réservés au propriétaire.
func requireOwner(l *List, userID string) error {
	if l.OwnerID != userID {
		return errForbidden
	}
	return nil
}

// visibleLists : toutes les listes que userID peut voir, avec leurs éléments.
func visibleLists(q querier, userID string) ([]*List, error) {
	rows, err := q.Query(listSelect+` ORDER BY l.owner_id <> ?, l.position, l.created_at`, userID)
	if err != nil {
		return nil, err
	}
	var lists []*List
	byID := map[string]*List{}
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if visibleTo(l, userID) {
			l.Items = []Item{}
			lists = append(lists, l)
			byID[l.ID] = l
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = q.Query(itemSelect + ` ORDER BY position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		if l := byID[it.ListID]; l != nil {
			l.Items = append(l.Items, *it)
		}
	}
	return lists, rows.Err()
}

// ---------------------------------------------------------------------------
// Lectures

const listSelect = `SELECT l.id, l.owner_id, u.name, l.name, l.color, l.shared, l.position
	FROM lists l JOIN users u ON u.id = l.owner_id`

const itemSelect = `SELECT id, list_id, text, checked, position, updated_at FROM items`

type scanner interface{ Scan(...any) error }

func scanList(s scanner) (*List, error) {
	var l List
	err := s.Scan(&l.ID, &l.OwnerID, &l.OwnerName, &l.Name, &l.Color, &l.Shared, &l.Position)
	return &l, err
}

func scanItem(s scanner) (*Item, error) {
	var it Item
	err := s.Scan(&it.ID, &it.ListID, &it.Text, &it.Checked, &it.Position, &it.UpdatedAt)
	return &it, err
}

func loadList(q querier, id string) (*List, error) {
	l, err := scanList(q.QueryRow(listSelect+` WHERE l.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return l, err
}

func loadItem(q querier, id string) (*Item, error) {
	it, err := scanItem(q.QueryRow(itemSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return it, err
}

func loadItems(q querier, listID string) ([]Item, error) {
	rows, err := q.Query(itemSelect+` WHERE list_id = ? ORDER BY position`, listID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *it)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------------------
// Écritures (appelées dans une transaction par App.write)

// Identifiant d'une création : dérivé d'un nonce aléatoire de 128 bits choisi par le
// client et de l'utilisateur. Le client connaît l'identifiant avant la réponse
// (mises à jour optimistes, file hors ligne) et un renvoi de la même requête est
// idempotent. Un autre utilisateur ne peut pas produire le même identifiant.
var nonceRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func creationID(userID, nonce string) (string, error) {
	if nonce == "" {
		return newID(), nil
	}
	if !nonceRe.MatchString(nonce) {
		return "", invalidError("nonce invalide")
	}
	h := sha256.Sum256([]byte("listes-id\x00" + userID + "\x00" + nonce))
	return hex.EncodeToString(h[:16]), nil
}

var idRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validID(id string) bool { return idRe.MatchString(id) }

// cleanText : UTF-8 valide, caractères de contrôle remplacés par des espaces, espaces
// de bord retirés, au plus max caractères.
func cleanText(s string, max int) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidError("texte invalide")
	}
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) > max {
		return "", invalidError("texte trop long")
	}
	return s, nil
}

func createList(tx querier, userID, nonce, name, color string) (l *List, created bool, err error) {
	id, err := creationID(userID, nonce)
	if err != nil {
		return nil, false, err
	}
	if name, err = cleanText(name, maxListName); err != nil {
		return nil, false, err
	}
	if name == "" {
		name = defaultListName
	}
	if color == "" {
		color = colors[0].Key
	} else if !validColor(color) {
		return nil, false, invalidError("couleur inconnue")
	}
	// Requête renvoyée (file hors ligne) : même résultat, sans doublon.
	if existing, err := accessList(tx, userID, id); err == nil {
		if existing.OwnerID == userID {
			return existing, false, nil
		}
		return nil, false, errNotFound
	} else if !errors.Is(err, errNotFound) {
		return nil, false, err
	}
	var n, pos int64
	if err := tx.QueryRow(`SELECT count(*), coalesce(max(position), 0) FROM lists WHERE owner_id = ?`, userID).Scan(&n, &pos); err != nil {
		return nil, false, err
	}
	if n >= maxListsPerUser {
		return nil, false, errLimit
	}
	t := nowMs()
	_, err = tx.Exec(`INSERT INTO lists (id, owner_id, name, color, shared, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, 0, ?, ?, ?)`, id, userID, name, color, pos+1, t, t)
	if isConstraint(err) {
		return nil, false, errNotFound // identifiant déjà pris par une liste invisible
	}
	if err != nil {
		return nil, false, err
	}
	l, err = loadList(tx, id)
	if err == nil {
		l.Items = []Item{}
	}
	return l, true, err
}

type listPatch struct {
	Name   *string `json:"name"`
	Color  *string `json:"color"`
	Shared *bool   `json:"shared"`
}

func updateList(tx querier, userID, listID string, p listPatch) (before, after *List, err error) {
	before, err = accessList(tx, userID, listID)
	if err != nil {
		return nil, nil, err
	}
	l := *before
	if p.Name != nil {
		name, err := cleanText(*p.Name, maxListName)
		if err != nil {
			return nil, nil, err
		}
		if name == "" {
			return nil, nil, invalidError("nom vide")
		}
		l.Name = name
	}
	if p.Color != nil {
		if err := requireOwner(before, userID); err != nil {
			return nil, nil, err
		}
		if !validColor(*p.Color) {
			return nil, nil, invalidError("couleur inconnue")
		}
		l.Color = *p.Color
	}
	if p.Shared != nil {
		if err := requireOwner(before, userID); err != nil {
			return nil, nil, err
		}
		l.Shared = *p.Shared
	}
	_, err = tx.Exec(`UPDATE lists SET name = ?, color = ?, shared = ?, updated_at = ? WHERE id = ?`,
		l.Name, l.Color, l.Shared, nowMs(), l.ID)
	if err != nil {
		return nil, nil, err
	}
	return before, &l, nil
}

func deleteList(tx querier, userID, listID string) (*List, error) {
	l, err := accessList(tx, userID, listID)
	if err != nil {
		return nil, err
	}
	if err := requireOwner(l, userID); err != nil {
		return nil, err
	}
	_, err = tx.Exec(`DELETE FROM lists WHERE id = ?`, l.ID)
	return l, err
}

func createItem(tx querier, userID, listID, nonce, text string) (it *Item, l *List, created bool, err error) {
	l, err = accessList(tx, userID, listID)
	if err != nil {
		return nil, nil, false, err
	}
	id, err := creationID(userID, nonce)
	if err != nil {
		return nil, nil, false, err
	}
	if text, err = cleanText(text, maxItemText); err != nil {
		return nil, nil, false, err
	}
	if text == "" {
		return nil, nil, false, invalidError("texte vide")
	}
	if existing, err := loadItem(tx, id); err == nil {
		if existing.ListID == l.ID {
			return existing, l, false, nil
		}
		return nil, nil, false, errNotFound
	} else if !errors.Is(err, errNotFound) {
		return nil, nil, false, err
	}
	var n, pos int64
	if err := tx.QueryRow(`SELECT count(*), coalesce(max(position), 0) FROM items WHERE list_id = ?`, l.ID).Scan(&n, &pos); err != nil {
		return nil, nil, false, err
	}
	if n >= maxItemsPerList {
		return nil, nil, false, errLimit
	}
	t := nowMs()
	if _, err := tx.Exec(`INSERT INTO items (id, list_id, text, checked, position, updated_at, updated_by)
		VALUES (?, ?, ?, 0, ?, ?, ?)`, id, l.ID, text, pos+1, t, userID); err != nil {
		return nil, nil, false, err
	}
	it, err = loadItem(tx, id)
	return it, l, true, err
}

type itemPatch struct {
	Text    *string `json:"text"`
	Checked *bool   `json:"checked"`
}

// updateItem : dernière écriture gagnante, élément par élément. Un texte vidé supprime l'élément.
func updateItem(tx querier, userID, itemID string, p itemPatch) (it *Item, l *List, deleted bool, err error) {
	it, l, err = accessItem(tx, userID, itemID)
	if err != nil {
		return nil, nil, false, err
	}
	if p.Text != nil {
		text, err := cleanText(*p.Text, maxItemText)
		if err != nil {
			return nil, nil, false, err
		}
		if text == "" {
			_, err = tx.Exec(`DELETE FROM items WHERE id = ?`, it.ID)
			return it, l, true, err
		}
		it.Text = text
	}
	if p.Checked != nil {
		it.Checked = *p.Checked
	}
	it.UpdatedAt = nowMs()
	_, err = tx.Exec(`UPDATE items SET text = ?, checked = ?, updated_at = ?, updated_by = ? WHERE id = ?`,
		it.Text, it.Checked, it.UpdatedAt, userID, it.ID)
	return it, l, false, err
}

type itemPosition struct {
	ID       string `json:"id"`
	Position int64  `json:"position"`
}

// moveItem place l'élément juste avant beforeID (ou en dernier si beforeID est vide),
// puis renumérote les positions de la liste. L'affichage regroupant cochés et non
// cochés, l'ordre relatif à l'intérieur de chaque groupe est celui voulu. beforeID
// doit être un élément de la même liste, retrouvée côté serveur.
func moveItem(tx querier, userID, itemID, beforeID string) (*List, []itemPosition, error) {
	it, l, err := accessItem(tx, userID, itemID)
	if err != nil {
		return nil, nil, err
	}
	if beforeID == itemID {
		return nil, nil, invalidError("position invalide")
	}
	items, err := loadItems(tx, l.ID)
	if err != nil {
		return nil, nil, err
	}
	order := make([]Item, 0, len(items))
	for _, x := range items {
		if x.ID != it.ID {
			order = append(order, x)
		}
	}
	at := len(order)
	if beforeID != "" {
		at = -1
		for i, x := range order {
			if x.ID == beforeID {
				at = i
			}
		}
		if at < 0 {
			return nil, nil, invalidError("position invalide")
		}
	}
	order = append(order[:at], append([]Item{*it}, order[at:]...)...)
	var changed []itemPosition
	for i, x := range order {
		if pos := int64(i + 1); x.Position != pos {
			if _, err := tx.Exec(`UPDATE items SET position = ? WHERE id = ?`, pos, x.ID); err != nil {
				return nil, nil, err
			}
			changed = append(changed, itemPosition{x.ID, pos})
		}
	}
	return l, changed, nil
}

func deleteItem(tx querier, userID, itemID string) (*Item, *List, error) {
	it, l, err := accessItem(tx, userID, itemID)
	if err != nil {
		return nil, nil, err
	}
	_, err = tx.Exec(`DELETE FROM items WHERE id = ?`, it.ID)
	return it, l, err
}

func clearChecked(tx querier, userID, listID string) (*List, []string, error) {
	l, err := accessList(tx, userID, listID)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.Query(`DELETE FROM items WHERE list_id = ? AND checked = 1 RETURNING id`, l.ID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	return l, ids, rows.Err()
}

func isConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}
