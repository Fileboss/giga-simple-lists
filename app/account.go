package main

// Pages « Mon compte » (mot de passe, appareils connectés) et lien à usage unique
// (invitation, réinitialisation, premier compte). Formulaires HTML classiques, sans JS :
// POST protégés comme le reste (Origin, Sec-Fetch-Site, jeton CSRF), puis redirection.

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// pageData : données communes des pages de compte et d'administration.
type pageData struct {
	Title       string
	Name, CSRF  string
	UserID      string
	IsAdmin     bool
	Flash       string
	Error       string
	MinPassword int

	Sessions []sessionInfo // mon compte

	Accounts   []account    // administration
	Links      []invitation // liens en attente
	NewLink    string       // lien qui vient d'être créé, affiché une seule fois
	NewLinkFor string

	Invite *invitation // page d'un lien
}

func (a *App) basePage(s *session, title string) pageData {
	p := pageData{Title: title, MinPassword: minPasswordRunes}
	if s != nil {
		p.Name, p.CSRF, p.UserID, p.IsAdmin = s.UserName, s.CSRF, s.UserID, s.IsAdmin
	}
	return p
}

// ---------------------------------------------------------------------------
// Mon compte

func (a *App) accountPage(w http.ResponseWriter, r *http.Request, s *session) {
	a.renderAccount(w, http.StatusOK, s, flashFromQuery(r), "")
}

// Messages après redirection : une clé connue seulement (jamais de texte venu de l'URL).
var flashKeys = map[string]string{
	"password": "account.passwordChanged", "revoked": "account.sessionRevoked", "others": "account.othersRevoked",
	"invite": "admin.linkDeleted", "role": "admin.roleChanged", "signout": "admin.signedOut", "deleted": "admin.userDeleted",
}

func flashFromQuery(r *http.Request) string {
	if k, ok := flashKeys[r.URL.Query().Get("done")]; ok {
		return T(k)
	}
	return ""
}

func (a *App) renderAccount(w http.ResponseWriter, status int, s *session, flash, errMsg string) {
	p := a.basePage(s, T("account.title"))
	p.Flash, p.Error = flash, errMsg
	var err error
	if p.Sessions, err = userSessions(a.db, s.UserID, s.Hash); err != nil {
		writeErr(w, err)
		return
	}
	a.render(w, status, "account.html", p)
}

// accountPassword : mot de passe actuel exigé (et compté comme une tentative de
// connexion : mêmes blocages), puis toutes les sessions du compte sont révoquées et cet
// appareil reçoit une nouvelle session : un ancien cookie volé ne sert plus.
func (a *App) accountPassword(w http.ResponseWriter, r *http.Request, s *session) {
	ip := clientIP(r, a.cfg.TrustedProxies)
	current, next := r.PostFormValue("current"), r.PostFormValue("password")
	if next != r.PostFormValue("confirm") {
		a.renderAccount(w, http.StatusBadRequest, s, "", T("err.passwordMismatch"))
		return
	}
	if err := validPassword(next); err != nil {
		a.renderAccount(w, http.StatusBadRequest, s, "", err.Error())
		return
	}
	start := time.Now()
	u := a.authenticate(ip, s.UserName, current)
	if d := a.loginMin - time.Since(start); d > 0 {
		time.Sleep(d)
	}
	if u == nil || u.ID != s.UserID {
		msg := T("err.currentPassword")
		if blocked, _ := loginBlocked(a.db, ipKey(ip)); blocked {
			msg = T("err.tooManyAttempts")
		}
		a.renderAccount(w, http.StatusUnauthorized, s, "", msg)
		return
	}
	hash := hashPassword(next) // hors du verrou d'écriture : Argon2id prend du temps
	revoked, err := a.inTx(func(tx *sql.Tx) ([][]byte, error) { return setPassword(tx, s.UserID, hash, nil) })
	if err != nil {
		writeErr(w, err)
		return
	}
	a.closeSessions(revoked)
	token, ns, err := createSession(a.db, s.UserID, r.UserAgent())
	if err != nil {
		writeErr(w, err)
		return
	}
	a.setSessionCookie(w, token, ns)
	slog.Info(T("log.passwordChanged"), "user", s.UserName, "ip", ip)
	http.Redirect(w, r, "/account?done=password", http.StatusSeeOther)
}

func (a *App) accountRevoke(w http.ResponseWriter, r *http.Request, s *session) {
	id := r.PathValue("id")
	if id == sessionID(s.Hash) {
		writeErr(w, errNotFound) // la session courante se ferme par « Se déconnecter »
		return
	}
	h, err := revokeSession(a.db, s.UserID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	a.closeSessions([][]byte{h})
	http.Redirect(w, r, "/account?done=revoked", http.StatusSeeOther)
}

func (a *App) accountRevokeOthers(w http.ResponseWriter, r *http.Request, s *session) {
	revoked, err := revokeSessions(a.db, s.UserID, s.Hash)
	if err != nil {
		writeErr(w, err)
		return
	}
	a.closeSessions(revoked)
	http.Redirect(w, r, "/account?done=others", http.StatusSeeOther)
}

// inTx : fn dans une transaction, sous le verrou d'écriture (comme commitAndPublish).
func (a *App) inTx(fn func(tx *sql.Tx) ([][]byte, error)) ([][]byte, error) {
	var out [][]byte
	err := a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		var err error
		out, err = fn(tx)
		return nil, err
	})
	return out, err
}

// closeSessions coupe les flux temps réel de sessions révoquées (sinon coupés au
// battement suivant, sous 20 s).
func (a *App) closeSessions(hashes [][]byte) {
	for _, h := range hashes {
		a.hub.closeSession(h)
	}
}

// ---------------------------------------------------------------------------
// Lien à usage unique : /invite/<jeton>. Sans session : formulaire protégé comme celui
// de connexion (Origin + jeton CSRF en double soumission).

func (a *App) invitePage(w http.ResponseWriter, r *http.Request) {
	inv, err := lookupInvitation(a.db, r.PathValue("token"))
	if err != nil {
		a.renderInviteGone(w, err)
		return
	}
	a.renderInvite(w, http.StatusOK, inv, "")
}

func (a *App) renderInviteGone(w http.ResponseWriter, err error) {
	if !errors.Is(err, errNotFound) {
		writeErr(w, err)
		return
	}
	p := a.basePage(nil, T("invite.goneTitle"))
	p.Error = T("invite.gone")
	a.render(w, http.StatusNotFound, "invite.html", p)
}

func (a *App) renderInvite(w http.ResponseWriter, status int, inv *invitation, errMsg string) {
	tok := randomToken()
	a.setCookie(w, loginCSRFCookie, tok, 3600, http.SameSiteStrictMode)
	title := T("invite.title")
	if inv.Kind == kindReset {
		title = T("reset.title")
	}
	p := a.basePage(nil, title)
	p.CSRF, p.Invite, p.Error = tok, inv, errMsg
	// Le jeton est dans l'URL : la page ne doit pas le transmettre à un autre site.
	// same-origin et non no-referrer : avec no-referrer, le navigateur envoie
	// « Origin: null » au formulaire, refusé par sameOrigin.
	w.Header().Set("Referrer-Policy", "same-origin")
	a.render(w, status, "invite.html", p)
}

func (a *App) invitePost(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r, a.cfg.TrustedProxies)
	if !a.sameOrigin(r) || r.ParseForm() != nil {
		writeJSON(w, http.StatusForbidden, apiError{T("err.refused")})
		return
	}
	inv, err := lookupInvitation(a.db, r.PathValue("token"))
	if err != nil {
		a.renderInviteGone(w, err)
		return
	}
	// Cookie du formulaire expiré (1 h) ou remplacé par la page de connexion d'un autre
	// onglet : on réaffiche le formulaire avec un nouveau jeton.
	c, err := r.Cookie(a.cookieName(loginCSRFCookie))
	if err != nil || subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(c.Value)) != 1 {
		a.renderInvite(w, http.StatusForbidden, inv, T("login.expired"))
		return
	}
	password := r.PostFormValue("password")
	if password != r.PostFormValue("confirm") {
		a.renderInvite(w, http.StatusBadRequest, inv, T("err.passwordMismatch"))
		return
	}
	if err := validPassword(password); err != nil {
		a.renderInvite(w, http.StatusBadRequest, inv, err.Error())
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	hash := hashPassword(password) // hors du verrou d'écriture : Argon2id prend du temps

	var userID string
	var revoked [][]byte
	err = a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		if err := consumeInvitation(tx, inv.ID); err != nil {
			return nil, err
		}
		var err error
		if inv.Kind == kindInvite {
			userID, err = insertUser(tx, name, hash, inv.IsAdmin)
			return nil, err
		}
		userID = inv.UserID
		if _, err = tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, userID); err != nil {
			return nil, err
		}
		if revoked, err = revokeSessions(tx, userID, nil); err != nil {
			return nil, err
		}
		_, err = tx.Exec(`DELETE FROM login_attempts WHERE key = ?`, userKey(inv.UserName))
		return nil, err
	})
	var inv2 invalidError
	switch {
	case errors.As(err, &inv2):
		a.renderInvite(w, http.StatusBadRequest, inv, inv2.Error())
		return
	case err != nil:
		a.renderInviteGone(w, err)
		return
	}
	a.closeSessions(revoked)
	// Navigateur déjà connecté (à un autre compte, ou un admin qui essaie le lien) : son
	// ancienne session est détruite, comme à la connexion.
	if old, err := r.Cookie(a.cookieName(sessionCookie)); err == nil {
		if h := tokenHash(old.Value); deleteSession(a.db, h) == nil {
			a.closeSessions([][]byte{h})
		}
	}
	token, s, err := createSession(a.db, userID, r.UserAgent())
	if err != nil {
		writeErr(w, err)
		return
	}
	if inv.Kind == kindInvite {
		slog.Info(T("log.accountCreated"), "user", name, "admin", inv.IsAdmin, "ip", ip)
	} else {
		slog.Info(T("log.passwordReset"), "user", inv.UserName, "ip", ip)
	}
	a.clearCookie(w, loginCSRFCookie)
	a.setSessionCookie(w, token, s)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
