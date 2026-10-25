package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// ---------------------------------------------------------------------------
// Mots de passe : Argon2id, paramètres de l'OWASP Password Storage Cheat Sheet
// (m = 19 Mio, t = 2, p = 1), stockés au format PHC.

const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// Au plus 2 calculs Argon2id simultanés : 19 Mio chacun, sous la limite de 128 Mo du conteneur.
var argonSem = make(chan struct{}, 2)

func argonKey(pw, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	argonSem <- struct{}{}
	key := argon2.IDKey(pw, salt, t, m, p, n)
	<-argonSem
	releaseMemorySoon()
	return key
}

// releaseMemorySoon rend au système, 2 s après le dernier calcul Argon2id, la mémoire
// qu'il a utilisée : Go la garderait sinon plusieurs minutes (RAM au repos < 30 Mo).
var (
	releaseMu    sync.Mutex
	releaseTimer *time.Timer
)

func releaseMemorySoon() {
	releaseMu.Lock()
	defer releaseMu.Unlock()
	if releaseTimer != nil {
		releaseTimer.Stop()
	}
	releaseTimer = time.AfterFunc(2*time.Second, debug.FreeOSMemory)
}

func hashPassword(pw string) string {
	salt := make([]byte, argonSaltLen)
	rand.Read(salt)
	key := argonKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

func verifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m > 256*1024 || t > 10 || p == 0 {
		return false
	}
	b64 := base64.RawStdEncoding
	salt, err1 := b64.DecodeString(parts[4])
	want, err2 := b64.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(want) == 0 {
		return false
	}
	got := argonKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// Empreinte factice : un nom inconnu coûte le même calcul qu'un nom connu.
var dummyHash = sync.OnceValue(func() string { return hashPassword(newID()) })

// ---------------------------------------------------------------------------
// Utilisateurs

type user struct {
	ID           string
	Name         string
	PasswordHash string
}

func userByName(q querier, name string) (*user, error) {
	var u user
	err := q.QueryRow(`SELECT id, name, password_hash FROM users WHERE name = ?`, name).
		Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	return &u, err
}

// ---------------------------------------------------------------------------
// Limitation des tentatives, stockée en base (survit aux redémarrages) :
//   - par IP : blocage, avec temporisation exponentielle ;
//   - par compte : simple ralentissement, plafonné à 1 minute. Un blocage par compte
//     permettrait à quiconque connaît un nom d'empêcher son propriétaire de se connecter.

type limitRule struct {
	threshold int64         // échecs tolérés avant blocage ou ralentissement
	base, max time.Duration // ensuite : base, 2×base, 4×base… plafonné à max
	block     bool          // bloquer (IP) plutôt que ralentir (compte)
}

func (r limitRule) delay(failures int64) time.Duration {
	if failures < r.threshold {
		return 0
	}
	if shift := failures - r.threshold; shift < 20 {
		return min(r.base<<shift, r.max)
	}
	return r.max
}

var (
	ipRule      = limitRule{threshold: 10, base: 30 * time.Second, max: time.Hour, block: true}
	accountRule = limitRule{threshold: 5, base: time.Second, max: time.Minute}
	// Un compteur sans échec depuis ce délai repart de zéro.
	attemptsWindow = 24 * time.Hour
)

func ipKey(ip string) string { return "ip:" + ip }

func userKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if len(name) > 64 {
		name = name[:64]
	}
	return "user:" + name
}

// loginBlocked : vrai si l'une des clés est en période de blocage (IP seulement).
func loginBlocked(q querier, keys ...string) (bool, error) {
	for _, k := range keys {
		var until int64
		err := q.QueryRow(`SELECT locked_until FROM login_attempts WHERE key = ?`, k).Scan(&until)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if until > nowMs() {
			return true, nil
		}
	}
	return false, nil
}

func recordFailure(q querier, key string, r limitRule) error {
	t := nowMs()
	var failures, last int64
	err := q.QueryRow(`SELECT failures, last_failure_at FROM login_attempts WHERE key = ?`, key).Scan(&failures, &last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if t-last > attemptsWindow.Milliseconds() {
		failures = 0
	}
	failures++
	var until int64
	if r.block {
		if d := r.delay(failures); d > 0 {
			until = t + d.Milliseconds()
		}
	}
	_, err = q.Exec(`INSERT INTO login_attempts (key, failures, last_failure_at, locked_until) VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET failures = excluded.failures, last_failure_at = excluded.last_failure_at,
		locked_until = excluded.locked_until`, key, failures, t, until)
	return err
}

// accountDelay : ralentissement à appliquer avant de vérifier une tentative sur ce compte.
func accountDelay(q querier, key string) (time.Duration, error) {
	var failures, last int64
	err := q.QueryRow(`SELECT failures, last_failure_at FROM login_attempts WHERE key = ?`, key).Scan(&failures, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if nowMs()-last > attemptsWindow.Milliseconds() {
		return 0, nil
	}
	return accountRule.delay(failures), nil
}

func clearFailures(q querier, keys ...string) error {
	for _, k := range keys {
		if _, err := q.Exec(`DELETE FROM login_attempts WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Sessions : jeton aléatoire de 256 bits dans le cookie, seul son SHA-256 en base.
// Expiration glissante de 90 jours, limite absolue de 180 jours.

const (
	sessionCookie = "__Host-session"
	sessionIdle   = 90 * 24 * time.Hour
	sessionMax    = 180 * 24 * time.Hour
	sessionTouch  = time.Hour // last_seen_at n'est réécrit qu'une fois par heure
)

type session struct {
	Hash     []byte
	UserID   string
	UserName string
	CSRF     string
	Created  int64
	LastSeen int64
}

func tokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func randomToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func createSession(q querier, userID string) (string, *session, error) {
	token := randomToken()
	t := nowMs()
	s := &session{Hash: tokenHash(token), UserID: userID, CSRF: randomToken(), Created: t, LastSeen: t}
	_, err := q.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		s.Hash, s.UserID, s.CSRF, s.Created, s.LastSeen)
	return token, s, err
}

// sessionExpiry : fin de validité d'une session (glissante, plafonnée).
func (s *session) expiry() int64 {
	return min(s.LastSeen+sessionIdle.Milliseconds(), s.Created+sessionMax.Milliseconds())
}

// lookupSession renvoie la session valide du jeton (et si last_seen_at vient d'être
// prolongé), ou errNotFound. Une session expirée est supprimée.
func lookupSession(q querier, token string) (*session, bool, error) {
	if token == "" || len(token) > 100 {
		return nil, false, errNotFound
	}
	s := &session{Hash: tokenHash(token)}
	err := q.QueryRow(`SELECT s.user_id, u.name, s.csrf_token, s.created_at, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, s.Hash).
		Scan(&s.UserID, &s.UserName, &s.CSRF, &s.Created, &s.LastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, errNotFound
	}
	if err != nil {
		return nil, false, err
	}
	t := nowMs()
	if t >= s.expiry() {
		_, err := q.Exec(`DELETE FROM sessions WHERE token_hash = ?`, s.Hash)
		if err != nil {
			return nil, false, err
		}
		return nil, false, errNotFound
	}
	if t-s.LastSeen >= sessionTouch.Milliseconds() {
		s.LastSeen = t
		if _, err := q.Exec(`UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?`, t, s.Hash); err != nil {
			return nil, false, err
		}
		return s, true, nil
	}
	return s, false, nil
}

// sessionAlive : la session existe encore et n'a pas expiré (revérifié par le flux SSE).
func sessionAlive(q querier, hash []byte) bool {
	var created, last int64
	err := q.QueryRow(`SELECT created_at, last_seen_at FROM sessions WHERE token_hash = ?`, hash).Scan(&created, &last)
	if err != nil {
		return false
	}
	s := session{Created: created, LastSeen: last}
	return nowMs() < s.expiry()
}

func deleteSession(q querier, hash []byte) error {
	_, err := q.Exec(`DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}
