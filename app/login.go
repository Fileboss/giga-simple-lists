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

const loginFailed = "Identifiant ou mot de passe incorrect."

type loginPageData struct {
	CSRF, Error string
}

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if _, _, err := lookupSession(a.db, c.Value); err == nil {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
	}
	a.renderLogin(w, http.StatusOK, "")
}

// renderLogin pose un nouveau jeton CSRF de connexion (double soumission : cookie + champ caché).
func (a *App) renderLogin(w http.ResponseWriter, status int, msg string) {
	tok := randomToken()
	setCookie(w, loginCSRFCookie, tok, 3600, http.SameSiteStrictMode)
	a.render(w, status, "login.html", loginPageData{CSRF: tok, Error: msg})
}

// Tentatives ralenties qui attendent en même temps : chacune garde une connexion ouverte.
var slowSem = make(chan struct{}, 32)

func (a *App) loginPost(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, a.cfg.TrustedProxies)

	c, err := r.Cookie(loginCSRFCookie)
	if !a.sameOrigin(r) || err != nil || r.ParseForm() != nil ||
		subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(c.Value)) != 1 {
		slog.Warn("connexion refusée : origine ou jeton CSRF invalide", "ip", ip)
		// Formulaire HTML classique : on réaffiche la page (cas typique : jeton expiré après 1 h).
		a.renderLogin(w, http.StatusForbidden, "Page expirée, veuillez réessayer.")
		return
	}

	name := r.PostFormValue("name")
	// Ralentissement par compte (après plusieurs échecs) : appliqué avant toute
	// vérification, au plus 1 minute ; le délai d'écriture est prolongé d'autant.
	delay, err := accountDelay(a.db, userKey(name))
	if err != nil {
		errorLog("login_attempts", err)
	}
	if delay > 0 {
		select {
		case slowSem <- struct{}{}:
		default:
			// Trop de tentatives ralenties en attente : refus immédiat, sans vérifier.
			throttledLog("connexion refusée : trop de tentatives ralenties en attente", "ip", ip)
			a.renderLogin(w, http.StatusUnauthorized, loginFailed)
			return
		}
		http.NewResponseController(w).SetWriteDeadline(time.Now().Add(delay + a.loginMin + 15*time.Second))
		t := time.NewTimer(delay)
		select {
		case <-t.C:
		case <-r.Context().Done():
			// Client parti : on libère la place sans vérifier.
			t.Stop()
			<-slowSem
			return
		}
		<-slowSem
	}
	start := time.Now()
	u := a.authenticate(ip, name, r.PostFormValue("password"))
	// Même durée pour toutes les issues : compte inexistant, mot de passe faux, IP bloquée.
	if d := a.loginMin - time.Since(start); d > 0 {
		time.Sleep(d)
	}
	if u == nil {
		a.renderLogin(w, http.StatusUnauthorized, loginFailed)
		return
	}

	// Nouvelle session à chaque connexion ; l'éventuelle ancienne est détruite.
	if old, err := r.Cookie(sessionCookie); err == nil {
		deleteSession(a.db, tokenHash(old.Value))
	}
	token, s, err := createSession(a.db, u.ID)
	if err != nil {
		errorLog("création de session", err)
		writeErr(w, err)
		return
	}
	slog.Info("connexion réussie", "ip", ip, "user", u.Name)
	clearCookie(w, loginCSRFCookie)
	setSessionCookie(w, token, s)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// authenticate vérifie le mot de passe. Le calcul coûteux est fait même pour un
// compte inexistant. Renvoie nil en cas d'échec ; la cause ne va qu'au journal.
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
		throttledLog("connexion refusée : IP bloquée (trop de tentatives)", "ip", ip)
		return nil
	case len(password) > 1024:
		reason = "mot de passe trop long"
	default:
		pwOK := verifyPassword(hash, password)
		switch {
		case u == nil:
			reason = "compte inconnu"
		case !pwOK:
			reason = "mot de passe"
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
	slog.Warn("connexion refusée", "ip", ip, "user", logName, "cause", reason)
	return nil
}

func (a *App) logout(w http.ResponseWriter, r *http.Request, s *session) {
	if err := deleteSession(a.db, s.Hash); err != nil {
		errorLog("déconnexion", err)
	}
	a.hub.closeSession(s.Hash)
	clearCookie(w, sessionCookie)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
