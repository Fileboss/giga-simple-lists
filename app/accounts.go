package main

// Comptes gérés depuis l'appli : rôle admin, liens à usage unique (invitation,
// réinitialisation du mot de passe), sessions de chaque compte. Les pages sont dans
// account.go (mon compte, liens) et adminweb.go (administration) ; la ligne de commande
// (admin.go) reste disponible.

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	inviteTTL         = 7 * 24 * time.Hour
	resetTTL          = 24 * time.Hour
	setupTTL          = 24 * time.Hour // premier compte (admin), lien écrit au démarrage
	maxPendingLinks   = 50
	minPasswordRunes  = 15
	maxPasswordBytes  = 1024
	kindInvite        = "invite"
	kindReset         = "reset"
	sessionIDHexBytes = 32 // identifiant d'une session dans les pages : hex du SHA-256 du jeton
)

// errLastAdmin : l'instance garde toujours au moins un admin.
var errLastAdmin = errors.New("dernier admin")

func validPassword(pw string) error {
	if utf8.RuneCountInString(pw) < minPasswordRunes {
		return invalidError("err.passwordShort")
	}
	if len(pw) > maxPasswordBytes {
		return invalidError("err.passwordLong")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Liens à usage unique

type invitation struct {
	ID        string
	Kind      string
	IsAdmin   bool
	UserID    string // reset : le compte concerné
	UserName  string
	CreatedBy string // nom de l'admin ; vide pour le lien du premier compte
	CreatedAt int64
	ExpiresAt int64
}

// createInvitation : nouveau lien (jeton de 256 bits, seul son hash est stocké). Un lien
// de réinitialisation remplace le précédent du même compte.
func createInvitation(q querier, kind string, isAdmin bool, userID, createdBy string, ttl time.Duration) (string, error) {
	if kind == kindReset {
		if _, err := q.Exec(`DELETE FROM invitations WHERE kind = 'reset' AND user_id = ?`, userID); err != nil {
			return "", err
		}
	}
	var n int
	if err := q.QueryRow(`SELECT count(*) FROM invitations WHERE expires_at > ?`, nowMs()).Scan(&n); err != nil {
		return "", err
	}
	if n >= maxPendingLinks {
		return "", errLimit
	}
	token := randomToken()
	t := nowMs()
	_, err := q.Exec(`INSERT INTO invitations (id, token_hash, kind, is_admin, user_id, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, newID(), tokenHash(token), kind, isAdmin, nullable(userID), nullable(createdBy), t, t+ttl.Milliseconds())
	return token, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

const invitationSelect = `SELECT i.id, i.kind, i.is_admin, coalesce(i.user_id, ''), coalesce(u.name, ''),
	coalesce(c.name, ''), i.created_at, i.expires_at
	FROM invitations i LEFT JOIN users u ON u.id = i.user_id LEFT JOIN users c ON c.id = i.created_by`

func scanInvitation(s scanner) (*invitation, error) {
	var inv invitation
	err := s.Scan(&inv.ID, &inv.Kind, &inv.IsAdmin, &inv.UserID, &inv.UserName, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt)
	return &inv, err
}

// lookupInvitation : le lien valide de ce jeton, ou errNotFound (inconnu ou expiré).
func lookupInvitation(q querier, token string) (*invitation, error) {
	if token == "" || len(token) > 100 {
		return nil, errNotFound
	}
	inv, err := scanInvitation(q.QueryRow(invitationSelect+` WHERE i.token_hash = ? AND i.expires_at > ?`, tokenHash(token), nowMs()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return inv, err
}

func pendingInvitations(q querier) ([]invitation, error) {
	rows, err := q.Query(invitationSelect+` WHERE i.expires_at > ? ORDER BY i.created_at DESC`, nowMs())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []invitation
	for rows.Next() {
		inv, err := scanInvitation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

func deleteInvitation(q querier, id string) error {
	_, err := q.Exec(`DELETE FROM invitations WHERE id = ?`, id)
	return err
}

// consumeInvitation : le lien ne sert qu'une fois, même si deux envois arrivent ensemble.
func consumeInvitation(q querier, id string) error {
	res, err := q.Exec(`DELETE FROM invitations WHERE id = ? AND expires_at > ?`, id, nowMs())
	if n, _ := rowsAffected(res, err); err == nil && n != 1 {
		return errNotFound
	}
	return err
}

// setupInvitation : sans aucun compte, un lien pour créer le premier, admin. Il est
// renouvelé à chaque démarrage (l'ancien ne sert plus) tant qu'aucun compte n'existe.
func setupInvitation(db *sql.DB) (string, error) {
	tx, err := db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil || n > 0 {
		return "", err
	}
	if _, err := tx.Exec(`DELETE FROM invitations WHERE kind = 'invite' AND created_by IS NULL`); err != nil {
		return "", err
	}
	token, err := createInvitation(tx, kindInvite, true, "", "", setupTTL)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// ---------------------------------------------------------------------------
// Comptes

type account struct {
	ID        string
	Name      string
	IsAdmin   bool
	CreatedAt int64
	Sessions  int
	LastSeen  int64 // 0 : jamais connecté
}

func accounts(q querier) ([]account, error) {
	rows, err := q.Query(`SELECT u.id, u.name, u.is_admin, u.created_at, count(s.token_hash), coalesce(max(s.last_seen_at), 0)
		FROM users u LEFT JOIN sessions s ON s.user_id = u.id AND `+liveSession+`
		GROUP BY u.id ORDER BY u.name COLLATE NOCASE`, sessionCutoffs()...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []account
	for rows.Next() {
		var a account
		if err := rows.Scan(&a.ID, &a.Name, &a.IsAdmin, &a.CreatedAt, &a.Sessions, &a.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func accountByID(q querier, id string) (*account, error) {
	var a account
	err := q.QueryRow(`SELECT id, name, is_admin, created_at FROM users WHERE id = ?`, id).Scan(&a.ID, &a.Name, &a.IsAdmin, &a.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return &a, err
}

// insertUser : nom valide et libre (sans tenir compte de la casse, accents compris).
func insertUser(tx *sql.Tx, name, passwordHash string, isAdmin bool) (string, error) {
	if err := validUserName(name); err != nil {
		return "", invalidError(err.Error())
	}
	taken, err := nameTaken(tx, name)
	if err != nil {
		return "", err
	}
	if taken {
		return "", invalidError("err.nameTaken")
	}
	id := newID()
	_, err = tx.Exec(`INSERT INTO users (id, name, password_hash, created_at, is_admin) VALUES (?, ?, ?, ?, ?)`,
		id, name, passwordHash, nowMs(), isAdmin)
	if isConstraint(err) {
		return "", invalidError("err.nameTaken")
	}
	return id, err
}

func otherAdmins(q querier, userID string) (int, error) {
	var n int
	err := q.QueryRow(`SELECT count(*) FROM users WHERE is_admin = 1 AND id <> ?`, userID).Scan(&n)
	return n, err
}

// setAdmin : donner ou retirer le rôle admin, sans jamais retirer le dernier. Le retrait
// tient en une requête (deux retraits simultanés ne peuvent pas passer tous les deux) et
// supprime les liens en attente créés par ce compte : il ne peut plus s'en servir pour
// redevenir admin ou prendre le compte d'un autre.
func setAdmin(tx *sql.Tx, userID string, admin bool) error {
	var res sql.Result
	var err error
	if admin {
		res, err = tx.Exec(`UPDATE users SET is_admin = 1 WHERE id = ?`, userID)
	} else {
		res, err = tx.Exec(`UPDATE users SET is_admin = 0 WHERE id = ?
			AND EXISTS (SELECT 1 FROM users WHERE is_admin = 1 AND id <> ?)`, userID, userID)
	}
	n, err := rowsAffected(res, err)
	if err != nil {
		return err
	}
	if n != 1 {
		if _, err := accountByID(tx, userID); err != nil {
			return err
		}
		return errLastAdmin
	}
	if !admin {
		_, err = tx.Exec(`DELETE FROM invitations WHERE created_by = ?`, userID)
	}
	return err
}

// deleteUser supprime le compte, ses listes (et leurs éléments) et ses sessions. Renvoie
// les listes partagées supprimées (à retirer chez les autres) et les sessions coupées.
//
// memberOf : les listes des autres dont le compte était membre, rechargées après la
// suppression (sans lui), pour que leurs autres lecteurs voient la liste des membres à jour.
func deleteUser(tx *sql.Tx, userID string) (shared, memberOf []*List, sessions [][]byte, err error) {
	a, err := accountByID(tx, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	if a.IsAdmin {
		n, err := otherAdmins(tx, userID)
		if err != nil {
			return nil, nil, nil, err
		}
		if n == 0 {
			return nil, nil, nil, errLastAdmin
		}
	}
	rows, err := tx.Query(listSelect+` WHERE l.owner_id = ?`, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	var owned []*List
	for rows.Next() {
		l, err := scanList(rows)
		if err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		owned = append(owned, l)
	}
	rows.Close()
	for _, l := range owned {
		if err := attachMembers(tx, l); err != nil {
			return nil, nil, nil, err
		}
		if l.isShared() || l.Public {
			shared = append(shared, l)
		}
	}
	if sessions, err = sessionHashes(tx, userID, nil); err != nil {
		return nil, nil, nil, err
	}
	var ids []string
	rows, err = tx.Query(`SELECT list_id FROM list_members WHERE user_id = ?`, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if _, err := tx.Exec(`DELETE FROM users WHERE id = ?`, userID); err != nil {
		return nil, nil, nil, err
	}
	if _, err := tx.Exec(`DELETE FROM login_attempts WHERE key = ?`, userKey(a.Name)); err != nil {
		return nil, nil, nil, err
	}
	for _, id := range ids {
		l, err := loadList(tx, id)
		if err != nil {
			return nil, nil, nil, err
		}
		memberOf = append(memberOf, l)
	}
	return shared, memberOf, sessions, nil
}

// setPassword : nouveau mot de passe (déjà haché, hors du verrou d'écriture) ; le jeton d'API et toutes les sessions du compte sont révoqués sauf
// keep (la session de l'appareil qui fait le changement, ou aucune). Renvoie les sessions
// révoquées, dont il faut couper les flux temps réel.
func setPassword(tx *sql.Tx, userID, passwordHash string, keep []byte) ([][]byte, error) {
	res, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, passwordHash, userID)
	if n, _ := rowsAffected(res, err); err == nil && n != 1 {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := deleteAPIToken(tx, userID); err != nil {
		return nil, err
	}
	return revokeSessions(tx, userID, keep)
}

// ---------------------------------------------------------------------------
// Sessions d'un compte

type sessionInfo struct {
	ID        string // hex du hash du jeton : désigne la session sans révéler le jeton
	UserAgent string
	Created   int64
	LastSeen  int64
	Current   bool
}

// liveSession : sessions encore valides (les expirées attendent d'être purgées), avec
// sessionCutoffs pour paramètres.
const liveSession = `s.last_seen_at > ? AND s.created_at > ?`

func sessionCutoffs() []any {
	t := nowMs()
	return []any{t - sessionIdle.Milliseconds(), t - sessionMax.Milliseconds()}
}

func sessionID(hash []byte) string { return hex.EncodeToString(hash) }

func userSessions(q querier, userID string, current []byte) ([]sessionInfo, error) {
	rows, err := q.Query(`SELECT token_hash, user_agent, created_at, last_seen_at FROM sessions s
		WHERE user_id = ? AND `+liveSession+` ORDER BY last_seen_at DESC`, append([]any{userID}, sessionCutoffs()...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionInfo
	for rows.Next() {
		var h []byte
		var s sessionInfo
		if err := rows.Scan(&h, &s.UserAgent, &s.Created, &s.LastSeen); err != nil {
			return nil, err
		}
		s.ID, s.Current = sessionID(h), string(h) == string(current)
		out = append(out, s)
	}
	return out, rows.Err()
}

func sessionHashes(q querier, userID string, keep []byte) ([][]byte, error) {
	rows, err := q.Query(`SELECT token_hash FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		if string(h) != string(keep) {
			out = append(out, h)
		}
	}
	return out, rows.Err()
}

// revokeSessions : toutes les sessions du compte sauf keep (nil : toutes).
func revokeSessions(q querier, userID string, keep []byte) ([][]byte, error) {
	hashes, err := sessionHashes(q, userID, keep)
	if err != nil {
		return nil, err
	}
	for _, h := range hashes {
		if err := deleteSession(q, h); err != nil {
			return nil, err
		}
	}
	return hashes, nil
}

// revokeSession : une session du compte, désignée par son identifiant de page.
func revokeSession(q querier, userID, id string) ([]byte, error) {
	h, err := hex.DecodeString(id)
	if err != nil || len(h) != sessionIDHexBytes {
		return nil, errNotFound
	}
	res, err := q.Exec(`DELETE FROM sessions WHERE token_hash = ? AND user_id = ?`, h, userID)
	if n, _ := rowsAffected(res, err); err == nil && n != 1 {
		return nil, errNotFound
	}
	return h, err
}

// ---------------------------------------------------------------------------
// Affichage

// deviceName : « Firefox on macOS », d'après le User-Agent enregistré à la connexion.
func deviceName(ua string) string {
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(ua, x) {
				return true
			}
		}
		return false
	}
	browser := ""
	switch {
	case has("Edg/", "EdgA/", "EdgiOS/"):
		browser = "Edge"
	case has("Firefox/", "FxiOS/"):
		browser = "Firefox"
	case has("OPR/"):
		browser = "Opera"
	case has("Chrome/", "CriOS/"):
		browser = "Chrome"
	case has("Safari/"):
		browser = "Safari"
	}
	system := ""
	switch {
	case has("iPhone"):
		system = "iPhone"
	case has("iPad"):
		system = "iPad"
	case has("Android"):
		system = "Android"
	case has("CrOS"):
		system = "ChromeOS"
	case has("Macintosh", "Mac OS X"):
		system = "macOS"
	case has("Windows"):
		system = "Windows"
	case has("Linux"):
		system = "Linux"
	}
	switch {
	case browser != "" && system != "":
		return tf("device.on", browser, system)
	case browser != "":
		return browser
	case system != "":
		return system
	}
	return T("device.unknown")
}

// initial : première lettre du nom, en capitale, pour l'avatar.
func initial(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// deviceKind : « phone » ou « computer », pour l'icône d'une session.
func deviceKind(ua string) string {
	for _, s := range []string{"iPhone", "Android", "Mobile"} {
		if strings.Contains(ua, s) {
			return "phone"
		}
	}
	return "computer"
}

// ago : « 5 min ago », « 3 days ago »… au-delà d'un mois, la date.
func ago(ms int64) string {
	d := time.Duration(nowMs()-ms) * time.Millisecond
	switch {
	case d < 2*time.Minute:
		return T("time.justNow")
	case d < time.Hour:
		return tf("time.minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return tf("time.hours", int(d.Hours()))
	case d < 48*time.Hour:
		return T("time.yesterday")
	case d < 30*24*time.Hour:
		return tf("time.days", int(d.Hours()/24))
	}
	return day(ms)
}

func day(ms int64) string { return time.UnixMilli(ms).Format(T("time.dateFormat")) }

// until : délai restant d'un lien (« 6 days left »).
func until(ms int64) string {
	d := time.Duration(ms-nowMs()) * time.Millisecond
	if d < 48*time.Hour {
		return tf("time.hoursLeft", max(1, int(d.Hours())))
	}
	return tf("time.daysLeft", int(d.Hours()/24))
}
