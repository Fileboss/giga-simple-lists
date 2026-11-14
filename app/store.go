package main

import (
	"cmp"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"slices"
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
	maxBodyBytes     = 16 << 10
	maxUserNameRunes = 32
	maxClearIDs      = 200 // par requête « supprimer les cochés » (tient dans maxBodyBytes)
	maxRestoreItems  = 200 // par requête de restauration (le client découpe selon maxBodyBytes)
)

var (
	errNotFound  = errors.New("introuvable")
	errForbidden = errors.New("réservé au propriétaire de la liste")
	errLimit     = errors.New("limite atteinte")
)

// invalidError : requête invalide (400) ; la valeur est une clé de message (i18n.go).
type invalidError string

func (e invalidError) Error() string { return T(string(e)) }

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
	Category  bool   `json:"category"`
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

const itemSelect = `SELECT id, list_id, text, checked, category, position, updated_at FROM items`

type scanner interface{ Scan(...any) error }

func scanList(s scanner) (*List, error) {
	var l List
	err := s.Scan(&l.ID, &l.OwnerID, &l.OwnerName, &l.Name, &l.Color, &l.Shared, &l.Position)
	return &l, err
}

func scanItem(s scanner) (*Item, error) {
	var it Item
	err := s.Scan(&it.ID, &it.ListID, &it.Text, &it.Checked, &it.Category, &it.Position, &it.UpdatedAt)
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
// Identifiants et nonces : 128 bits en hexadécimal.
var hex128Re = regexp.MustCompile(`^[0-9a-f]{32}$`)

func creationID(userID, nonce string) (string, error) {
	if nonce == "" {
		return newID(), nil
	}
	if !hex128Re.MatchString(nonce) {
		return "", invalidError("err.nonce")
	}
	h := sha256.Sum256([]byte("listes-id\x00" + userID + "\x00" + nonce))
	return hex.EncodeToString(h[:16]), nil
}

func validID(id string) bool { return hex128Re.MatchString(id) }

// cleanText : UTF-8 valide, caractères de contrôle remplacés par des espaces, espaces
// de bord retirés, au plus max caractères.
func cleanText(s string, max int) (string, error) {
	if !utf8.ValidString(s) {
		return "", invalidError("err.text")
	}
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s))
	if utf8.RuneCountInString(s) > max {
		return "", invalidError("err.textTooLong")
	}
	return s, nil
}

// createList : shared et position (0 : en dernier) servent à restaurer une liste supprimée.
func createList(tx querier, userID, nonce, name, color string, shared bool, position int64) (l *List, created bool, err error) {
	id, err := creationID(userID, nonce)
	if err != nil {
		return nil, false, err
	}
	if name, err = cleanText(name, maxListName); err != nil {
		return nil, false, err
	}
	if name == "" {
		name = T("list.default")
	}
	if color == "" {
		color = colors[0].Key
	} else if !validColor(color) {
		return nil, false, invalidError("err.color")
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
	if position <= 0 {
		position = pos + 1
	}
	t := nowMs()
	_, err = tx.Exec(`INSERT INTO lists (id, owner_id, name, color, shared, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, userID, name, color, shared, position, t, t)
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
			return nil, nil, invalidError("err.nameEmpty")
		}
		l.Name = name
	}
	if p.Color != nil {
		if err := requireOwner(before, userID); err != nil {
			return nil, nil, err
		}
		if !validColor(*p.Color) {
			return nil, nil, invalidError("err.color")
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

func createItem(tx querier, userID, listID, nonce, text string, category bool) (it *Item, l *List, created bool, err error) {
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
		return nil, nil, false, invalidError("err.textEmpty")
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
	if _, err := tx.Exec(`INSERT INTO items (id, list_id, text, checked, category, position, updated_at, updated_by)
		VALUES (?, ?, ?, 0, ?, ?, ?, ?)`, id, l.ID, text, category, pos+1, t, userID); err != nil {
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
		if *p.Checked && it.Category {
			return nil, nil, false, invalidError("err.categoryCheck")
		}
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
// cochés dans chaque catégorie, l'ordre relatif à l'intérieur de chaque groupe est
// celui voulu. Une catégorie se déplace avec ses éléments, et seulement avant une
// autre catégorie (ou en dernier) : elle ne coupe jamais une autre en deux. beforeID
// doit être un élément de la même liste, retrouvée côté serveur.
func moveItem(tx querier, userID, itemID, beforeID string) (*List, []itemPosition, error) {
	it, l, err := accessItem(tx, userID, itemID)
	if err != nil {
		return nil, nil, err
	}
	if beforeID == itemID {
		return nil, nil, invalidError("err.position")
	}
	items, err := loadItems(tx, l.ID)
	if err != nil {
		return nil, nil, err
	}
	block, order := splitBlock(items, it.ID)
	at := len(order)
	if beforeID != "" {
		at = -1
		for i, x := range order {
			if x.ID == beforeID && (!it.Category || x.Category) {
				at = i
			}
		}
		if at < 0 {
			return nil, nil, invalidError("err.position")
		}
	}
	order = append(order[:at], append(block, order[at:]...)...)
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

// splitBlock sépare de items (triés par position) l'élément id, suivi de ses éléments
// si c'est une catégorie, et le reste de la liste.
func splitBlock(items []Item, id string) (block, rest []Item) {
	start := 0
	for i, x := range items {
		if x.ID == id {
			start = i
		}
	}
	end := start + 1
	if items[start].Category {
		for end < len(items) && !items[end].Category {
			end++
		}
	}
	block = append([]Item{}, items[start:end]...)
	rest = append(append([]Item{}, items[:start]...), items[end:]...)
	return block, rest
}

type restoredItem struct {
	Nonce    string `json:"nonce"`
	Text     string `json:"text"`
	Checked  bool   `json:"checked"`
	Category bool   `json:"category"`
	Position int64  `json:"position"`
}

// restoreItems recrée des éléments supprimés (annulation) à leur ancienne position :
// chacun y est inséré en décalant d'un cran ceux qui sont à cette position ou après.
// Insérés du plus bas au plus haut, ils reprennent tous leur place, même envoyés en
// plusieurs requêtes (les autres positions ne bougent qu'en bloc). Les identifiants
// viennent de nonces, comme une création : un renvoi (file hors ligne) ne recrée rien.
// Renvoie les éléments recréés et les positions des autres qui ont changé.
func restoreItems(tx querier, userID, listID string, in []restoredItem) (*List, []Item, []itemPosition, error) {
	if len(in) > maxRestoreItems {
		return nil, nil, nil, invalidError("err.tooManyItems")
	}
	l, err := accessList(tx, userID, listID)
	if err != nil {
		return nil, nil, nil, err
	}
	var add []Item
	seen := map[string]bool{}
	for _, r := range in {
		if r.Nonce == "" {
			return nil, nil, nil, invalidError("err.nonceMissing")
		}
		id, err := creationID(userID, r.Nonce)
		if err != nil {
			return nil, nil, nil, err
		}
		text, err := cleanText(r.Text, maxItemText)
		if err != nil {
			return nil, nil, nil, err
		}
		if text == "" {
			return nil, nil, nil, invalidError("err.textEmpty")
		}
		if r.Category && r.Checked {
			return nil, nil, nil, invalidError("err.categoryCheck")
		}
		if existing, err := loadItem(tx, id); err == nil {
			if existing.ListID != l.ID {
				return nil, nil, nil, errNotFound
			}
			continue // déjà restauré
		} else if !errors.Is(err, errNotFound) {
			return nil, nil, nil, err
		}
		if !seen[id] {
			seen[id] = true
			add = append(add, Item{ID: id, Text: text, Checked: r.Checked, Category: r.Category, Position: max(r.Position, 1)})
		}
	}
	before, err := loadItems(tx, l.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(before)+len(add) > maxItemsPerList {
		return nil, nil, nil, errLimit
	}
	slices.SortStableFunc(add, func(a, b Item) int { return cmp.Compare(b.Position, a.Position) })
	t := nowMs()
	for _, x := range add {
		if _, err := tx.Exec(`UPDATE items SET position = position + 1 WHERE list_id = ? AND position >= ?`, l.ID, x.Position); err != nil {
			return nil, nil, nil, err
		}
		if _, err := tx.Exec(`INSERT INTO items (id, list_id, text, checked, category, position, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, x.ID, l.ID, x.Text, x.Checked, x.Category, x.Position, t, userID); err != nil {
			return nil, nil, nil, err
		}
	}
	after, err := loadItems(tx, l.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	old := map[string]int64{}
	for _, x := range before {
		old[x.ID] = x.Position
	}
	restored := []Item{}
	var moved []itemPosition
	for _, x := range after {
		if seen[x.ID] {
			restored = append(restored, x)
		} else if p, ok := old[x.ID]; ok && p != x.Position {
			moved = append(moved, itemPosition{x.ID, x.Position})
		}
	}
	return l, restored, moved, nil
}

func deleteItem(tx querier, userID, itemID string) (*Item, *List, error) {
	it, l, err := accessItem(tx, userID, itemID)
	if err != nil {
		return nil, nil, err
	}
	_, err = tx.Exec(`DELETE FROM items WHERE id = ?`, it.ID)
	return it, l, err
}

// clearChecked supprime les éléments ids de la liste, s'ils sont toujours cochés. Le
// client envoie ceux qu'il voyait cochés : un renvoi tardif (file hors ligne) ne
// supprime pas ce qu'un autre a coché entre-temps.
func clearChecked(tx querier, userID, listID string, ids []string) (*List, []string, error) {
	if len(ids) > maxClearIDs {
		return nil, nil, invalidError("err.tooManyItems")
	}
	for _, id := range ids {
		if !validID(id) {
			return nil, nil, invalidError("err.item")
		}
	}
	l, err := accessList(tx, userID, listID)
	if err != nil {
		return nil, nil, err
	}
	removed := []string{}
	if len(ids) == 0 {
		return l, removed, nil
	}
	args := []any{l.ID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := tx.Query(`DELETE FROM items WHERE list_id = ? AND checked = 1 AND id IN (?`+
		strings.Repeat(", ?", len(ids)-1)+`) RETURNING id`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		removed = append(removed, id)
	}
	return l, removed, rows.Err()
}

func isConstraint(err error) bool {
	return err != nil && strings.Contains(err.Error(), "constraint failed")
}
