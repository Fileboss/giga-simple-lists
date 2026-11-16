package main

// Connexion et déconnexion : pages et handlers HTTP. Les primitives (mots de passe,
// sessions, compteurs d'échecs) sont dans auth.go.

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const loginCSRFCookie = "__Host-login"

type loginPageData struct {
	CSRF, Error string
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	var current *session
	if c, err := r.Cookie(a.cookieName(sessionCookie)); err == nil {
		current, _, _ = lookupSession(a.db, c.Value)
	}
	// Déjà authentifié par le reverse proxy : on garde la session de ce compte s'il en a
	// une (sinon un simple lien vers /login casserait les onglets ouverts).
	if u := a.headerUser(r); u != nil && (current == nil || current.UserID != u.ID) {
		if _, err := a.headerSession(w, r, u); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	} else if current != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	a.renderLogin(w, http.StatusOK, "")
}

// renderLogin pose un nouveau jeton CSRF de connexion (double soumission : cookie + champ caché).
func (a *App) renderLogin(w http.ResponseWriter, status int, msg string) {
	tok := randomToken()
	a.setCookie(w, loginCSRFCookie, tok, 3600, http.SameSiteStrictMode)
	a.render(w, status, "login.html", loginPageData{CSRF: tok, Error: msg})
}

// Tentatives ralenties qui attendent en même temps : chacune garde une connexion ouverte.
var slowSem = make(chan struct{}, 32)

func (a *App) loginPost(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, a.cfg.TrustedProxies)

	c, err := r.Cookie(a.cookieName(loginCSRFCookie))
	if !a.sameOrigin(r) || err != nil || r.ParseForm() != nil ||
		subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(c.Value)) != 1 {
		slog.Warn(T("log.loginRefusedCSRF"), "ip", ip, "origin", r.Header.Get("Origin"), "expected", a.cfg.Origin)
		// Formulaire HTML classique : on réaffiche la page (cas typique : jeton expiré après 1 h).
		// Page ouverte à une autre adresse que LISTES_ORIGIN (http://<ip>:5240 au lieu de
		// l'URL publique, erreur classique d'une première installation) : on le dit, sans
		// donner l'adresse configurée à n'importe quel visiteur.
		msg := T("login.expired")
		if o := r.Header.Get("Origin"); o != "" && o != a.cfg.Origin {
			msg = T("login.wrongOrigin") // l'adresse attendue reste dans les journaux, pas dans la page
		}
		a.renderLogin(w, http.StatusForbidden, msg)
		return
	}

	name := r.PostFormValue("name")
	switch a.waitAccountDelay(w, r, ip, name) {
	case slowQueueFull:
		a.renderLogin(w, http.StatusUnauthorized, T("login.failed"))
		return
	case slowClientGone:
		return
	}
	start := time.Now()
	u := a.authenticate(ip, name, r.PostFormValue("password"))
	// Même durée pour toutes les issues : compte inexistant, mot de passe faux, IP bloquée.
	if d := a.loginMin - time.Since(start); d > 0 {
		time.Sleep(d)
	}
	if u == nil {
		a.renderLogin(w, http.StatusUnauthorized, T("login.failed"))
		return
	}

	// Nouvelle session à chaque connexion ; l'éventuelle ancienne est détruite.
	if old, err := r.Cookie(a.cookieName(sessionCookie)); err == nil {
		deleteSession(a.db, tokenHash(old.Value))
	}
	token, s, err := createSession(a.db, u.ID, r.UserAgent())
	if err != nil {
		errorLog(T("log.sessionCreate"), err)
		writeErr(w, err)
		return
	}
	slog.Info(T("log.loginOK"), "ip", ip, "user", u.Name)
	a.clearCookie(w, loginCSRFCookie)
	a.setSessionCookie(w, token, s)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// authenticate vérifie le mot de passe. Le calcul coûteux est fait même pour un
// compte inexistant. Renvoie nil en cas d'échec ; la cause ne va qu'au journal.
type slowResult int

const (
	slowDone       slowResult = iota // délai écoulé (ou aucun) : on peut vérifier
	slowQueueFull                    // trop de tentatives ralenties en attente : refus sans vérifier
	slowClientGone                   // client parti pendant l'attente
)

// waitAccountDelay : ralentissement par compte (après plusieurs échecs), appliqué avant
// toute vérification du mot de passe, au plus 1 minute ; le délai d'écriture est prolongé
// d'autant. Un ralentissement plutôt qu'un refus : quiconque connaît le nom ne peut pas
// empêcher son propriétaire de se connecter ou de changer son mot de passe.
func (a *App) waitAccountDelay(w http.ResponseWriter, r *http.Request, ip, name string) slowResult {
	delay, err := accountDelay(a.db, userKey(name))
	if err != nil {
		errorLog("login_attempts", err)
	}
	if delay <= 0 {
		return slowDone
	}
	select {
	case slowSem <- struct{}{}:
	default:
		throttledLog(T("log.loginQueueFull"), "ip", ip)
		return slowQueueFull
	}
	defer func() { <-slowSem }()
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(delay + a.loginMin + 15*time.Second))
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return slowDone
	case <-r.Context().Done():
		return slowClientGone
	}
}

func (a *App) authenticate(ip, name, password string) *user {
	name = strings.TrimSpace(name)
	keys := []string{ipKey(ip), userKey(name)}
	blocked, err := loginBlocked(a.db, keys[0])
	if err != nil {
		errorLog("login_attempts", err)
		return nil
	}
	u, err := userByName(a.db, name)
	if err != nil && !errors.Is(err, errNotFound) {
		errorLog("users", err)
		return nil
	}
	logName := "" // nom journalisé seulement s'il existe (un mot de passe tapé dans le mauvais champ ne finit pas dans les journaux)
	hash := dummyHash()
	if u != nil {
		hash, logName = u.PasswordHash, u.Name
	}

	reason := ""
	switch {
	case blocked:
		// Une IP bloquée qui insiste ne remplit pas le journal : une ligne par minute au plus.
		throttledLog(T("log.loginBlockedIP"), "ip", ip)
		return nil
	case len(password) > 1024:
		reason = T("log.causePasswordLong")
	default:
		pwOK := verifyPassword(hash, password)
		switch {
		case u == nil:
			reason = T("log.causeUnknownAccount")
		case !pwOK:
			reason = T("log.causePassword")
		default:
			if err := clearFailures(a.db, keys...); err != nil {
				errorLog("login_attempts", err)
			}
			return u
		}
	}
	if err := recordFailure(a.db, keys[0], ipRule); err != nil {
		errorLog("login_attempts", err)
	}
	if err := recordFailure(a.db, keys[1], accountRule); err != nil {
		errorLog("login_attempts", err)
	}
	slog.Warn(T("log.loginRefused"), "ip", ip, "user", logName, T("log.cause"), reason)
	return nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request, s *session) {
	if err := deleteSession(a.db, s.Hash); err != nil {
		errorLog(T("log.logout"), err)
	}
	a.hub.closeSession(s.Hash)
	a.clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
