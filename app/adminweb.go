package main

// Page d'administration, réservée aux comptes admin : comptes (rôle, lien de
// réinitialisation, déconnexion, suppression) et liens d'invitation. Un compte non admin
// reçoit 404, comme pour une liste qu'il ne voit pas.

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
)

func (a *App) adminOnly(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return a.authedPage(func(w http.ResponseWriter, r *http.Request, s *session) {
		if !s.IsAdmin {
			http.NotFound(w, r)
			return
		}
		h(w, r, s)
	})
}

func (a *App) adminPage(w http.ResponseWriter, r *http.Request, s *session) {
	a.renderAdmin(w, http.StatusOK, s, func(p *pageData) { p.Flash = flashFromQuery(r) })
}

func (a *App) renderAdmin(w http.ResponseWriter, status int, s *session, with func(*pageData)) {
	p := a.basePage(s, T("admin.title"))
	var err error
	if p.Accounts, err = accounts(a.db); err == nil {
		p.Links, err = pendingInvitations(a.db)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if with != nil {
		with(&p)
	}
	a.render(w, status, "admin.html", p)
}

// adminError : message dans la page pour les refus attendus (dernier admin, limite…).
func (a *App) adminError(w http.ResponseWriter, s *session, err error) {
	var msg string
	var inv invalidError
	switch {
	case errors.Is(err, errLastAdmin):
		msg = T("admin.lastAdmin")
	case errors.Is(err, errLimit):
		msg = T("admin.tooManyLinks")
	case errors.Is(err, errNotFound):
		msg = T("err.notFound")
	case errors.As(err, &inv):
		msg = inv.Error()
	default:
		writeErr(w, err)
		return
	}
	a.renderAdmin(w, http.StatusBadRequest, s, func(p *pageData) { p.Error = msg })
}

// Le lien n'est affiché qu'ici, une fois : seul son hash est gardé.
func (a *App) showLink(w http.ResponseWriter, s *session, token, forWhat string) {
	a.renderAdmin(w, http.StatusOK, s, func(p *pageData) {
		p.NewLink, p.NewLinkFor = a.cfg.Origin+"/invite/"+token, forWhat
	})
}

func (a *App) adminInvite(w http.ResponseWriter, r *http.Request, s *session) {
	admin := r.PostFormValue("admin") == "on"
	token, err := createInvitation(a.db, kindInvite, admin, "", s.UserID, inviteTTL)
	if err != nil {
		a.adminError(w, s, err)
		return
	}
	slog.Info(T("log.inviteCreated"), "by", s.UserName, "admin", admin)
	forWhat := T("admin.newInvite")
	if admin {
		forWhat = T("admin.newAdminInvite")
	}
	a.showLink(w, s, token, forWhat)
}

func (a *App) adminDeleteLink(w http.ResponseWriter, r *http.Request, s *session) {
	if err := deleteInvitation(a.db, r.PathValue("id")); err != nil {
		writeErr(w, err)
		return
	}
	http.Redirect(w, r, "/admin?done=invite", http.StatusSeeOther)
}

// adminUser : le compte de l'URL, ou une erreur dans la page.
func (a *App) adminUser(w http.ResponseWriter, r *http.Request, s *session) *account {
	id := r.PathValue("id")
	if !validID(id) {
		a.adminError(w, s, errNotFound)
		return nil
	}
	u, err := accountByID(a.db, id)
	if err != nil {
		a.adminError(w, s, err)
		return nil
	}
	return u
}

func (a *App) adminResetLink(w http.ResponseWriter, r *http.Request, s *session) {
	u := a.adminUser(w, r, s)
	if u == nil {
		return
	}
	token, err := createInvitation(a.db, kindReset, false, u.ID, s.UserID, resetTTL)
	if err != nil {
		a.adminError(w, s, err)
		return
	}
	slog.Info(T("log.resetCreated"), "by", s.UserName, "user", u.Name)
	a.showLink(w, s, token, tf("admin.newReset", u.Name))
}

func (a *App) adminRole(w http.ResponseWriter, r *http.Request, s *session) {
	u := a.adminUser(w, r, s)
	if u == nil {
		return
	}
	admin := r.PostFormValue("admin") == "1"
	if _, err := a.inTx(func(tx *sql.Tx) ([][]byte, error) { return nil, setAdmin(tx, u.ID, admin) }); err != nil {
		a.adminError(w, s, err)
		return
	}
	slog.Info(T("log.roleChanged"), "by", s.UserName, "user", u.Name, "admin", admin)
	if u.ID == s.UserID && !admin {
		http.Redirect(w, r, "/", http.StatusSeeOther) // plus d'accès à cette page
		return
	}
	http.Redirect(w, r, "/admin?done=role", http.StatusSeeOther)
}

// adminSignOut : déconnecte tous les appareils du compte (sauf celui de l'admin, s'il
// s'agit de son propre compte).
func (a *App) adminSignOut(w http.ResponseWriter, r *http.Request, s *session) {
	u := a.adminUser(w, r, s)
	if u == nil {
		return
	}
	revoked, err := a.inTx(func(tx *sql.Tx) ([][]byte, error) { return revokeSessions(tx, u.ID, s.Hash) })
	if err != nil {
		writeErr(w, err)
		return
	}
	a.closeSessions(revoked)
	slog.Info(T("log.signedOut"), "by", s.UserName, "user", u.Name)
	http.Redirect(w, r, "/admin?done=signout", http.StatusSeeOther)
}

// adminDelete : le compte, ses listes et ses sessions. Ses listes partagées disparaissent
// chez les autres en temps réel. On ne supprime pas son propre compte d'ici.
func (a *App) adminDelete(w http.ResponseWriter, r *http.Request, s *session) {
	u := a.adminUser(w, r, s)
	if u == nil {
		return
	}
	if u.ID == s.UserID {
		a.adminError(w, s, invalidError("admin.notSelf"))
		return
	}
	var sessions [][]byte
	err := a.commitAndPublish(func(tx *sql.Tx) ([]event, error) {
		shared, memberOf, hashes, err := deleteUser(tx, u.ID)
		if err != nil {
			return nil, err
		}
		sessions = hashes
		evs := make([]event, 0, len(shared)+len(memberOf))
		for _, l := range shared {
			evs = append(evs, newEvent(evListRemoved, removal{ID: l.ID, Reason: reasonDeleted}, toViewersOf(l)))
		}
		for _, l := range memberOf { // membres à jour chez ceux qui la voient encore
			meta := *l
			meta.Items = nil
			evs = append(evs, newEvent(evList, meta, toViewersOf(l)))
		}
		return evs, nil
	})
	if err != nil {
		a.adminError(w, s, err)
		return
	}
	a.closeSessions(sessions)
	slog.Info(T("log.userDeleted"), "by", s.UserName, "user", u.Name)
	http.Redirect(w, r, "/admin?done=deleted", http.StatusSeeOther)
}
