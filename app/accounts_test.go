package main

import (
	"bytes"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// form : POST d'un formulaire de page par un navigateur légitime (session, Origin, CSRF).
func (h *harness) form(s *testSession, path string, v url.Values) *httptest.ResponseRecorder {
	if s != nil {
		v.Set("csrf", s.CSRF)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.RemoteAddr = "203.0.113.7:4444"
	if s != nil {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.Token})
	}
	return h.serve(req)
}

var linkRe = regexp.MustCompile(`/invite/([A-Za-z0-9_-]{43})`)

// newLink : l'admin crée un lien depuis la page ; renvoie le jeton affiché.
func (h *harness) newLink(admin *testSession, path string, v url.Values) string {
	h.t.Helper()
	w := h.form(admin, path, v)
	m := linkRe.FindStringSubmatch(w.Body.String())
	if w.Code != http.StatusOK || m == nil {
		h.t.Fatalf("%s : %d %s", path, w.Code, w.Body.String())
	}
	return m[1]
}

// useLink : ouvre la page du lien puis envoie le formulaire, comme un navigateur.
func (h *harness) useLink(token, name, password string) *httptest.ResponseRecorder {
	h.t.Helper()
	w := h.serve(httptest.NewRequest("GET", "/invite/"+token, nil))
	if w.Code != http.StatusOK {
		return w
	}
	if w.Header().Get("Referrer-Policy") != "same-origin" {
		h.t.Fatal("page du lien sans Referrer-Policy: same-origin")
	}
	var csrf *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == loginCSRFCookie {
			csrf = c
		}
	}
	v := url.Values{"csrf": {csrf.Value}, "name": {name}, "password": {password}, "confirm": {password}}
	req := httptest.NewRequest("POST", "/invite/"+token, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(csrf)
	return h.serve(req)
}

func sessionFrom(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

func isAdmin(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var a bool
	if err := db.QueryRow(`SELECT is_admin FROM users WHERE name = ?`, name).Scan(&a); err != nil {
		t.Fatal(err)
	}
	return a
}

const newPassword = "another-long-passphrase-42"

// Le premier compte (ligne de commande) est admin, les suivants non.
func TestFirstCLIAccountIsAdmin(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	h.addUser("bob")
	if !isAdmin(t, h.db, "alice") || isAdmin(t, h.db, "bob") {
		t.Fatal("rôles")
	}
}

// Instance existante : le plus ancien compte devient admin, les sessions restent valides.
func TestMigrationAccounts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "listes.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	// Revenir à l'état de la version 3 : sans ce qu'ajoutent 004 et 005.
	for _, q := range []string{`DROP TABLE list_members`, `DROP INDEX lists_public_token`, `ALTER TABLE lists DROP COLUMN public_token`,
		`DROP TABLE invitations`, `ALTER TABLE sessions DROP COLUMN user_agent`,
		`ALTER TABLE users DROP COLUMN is_admin`, `PRAGMA user_version = 3`,
		`INSERT INTO users (id, name, password_hash, created_at) VALUES ('` + newID() + `', 'zoe', 'x', 2000)`,
		`INSERT INTO users (id, name, password_hash, created_at) VALUES ('` + newID() + `', 'alice', 'x', 1000)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	db.Close()
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !isAdmin(t, db, "alice") || isAdmin(t, db, "zoe") {
		t.Fatal("le plus ancien compte doit être le seul admin")
	}
}

func TestSetupInvitation(t *testing.T) {
	h := newHarness(t)
	first, err := setupInvitation(h.db)
	if err != nil || first == "" {
		t.Fatalf("%q %v", first, err)
	}
	second, _ := setupInvitation(h.db) // redémarrage : nouveau lien, l'ancien ne sert plus
	if w := h.serve(httptest.NewRequest("GET", "/invite/"+first, nil)); w.Code != http.StatusNotFound {
		t.Fatalf("ancien lien : %d", w.Code)
	}
	w := h.useLink(second, "alice", testPassword)
	if w.Code != http.StatusSeeOther || sessionFrom(w) == "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !isAdmin(t, h.db, "alice") {
		t.Fatal("le premier compte doit être admin")
	}
	if tok, _ := setupInvitation(h.db); tok != "" {
		t.Fatal("lien de premier compte alors qu'un compte existe")
	}
}

func TestInvitationFlow(t *testing.T) {
	h := newHarness(t)
	admin := h.session(h.addUser("alice"))
	token := h.newLink(admin, "/admin/invites", url.Values{})

	// Nom déjà pris : refus, le lien reste valable.
	if w := h.useLink(token, "ALICE", testPassword); w.Code != http.StatusBadRequest {
		t.Fatalf("nom pris : %d", w.Code)
	}
	if w := h.useLink(token, "bob", "court"); w.Code != http.StatusBadRequest {
		t.Fatalf("mot de passe court : %d", w.Code)
	}
	w := h.useLink(token, "bob", testPassword)
	if w.Code != http.StatusSeeOther || sessionFrom(w) == "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if isAdmin(t, h.db, "bob") {
		t.Fatal("invitation simple : pas admin")
	}
	if res := h.loginForm("198.51.100.9", "bob", testPassword); res.Code != http.StatusSeeOther {
		t.Fatal("connexion du nouveau compte")
	}
	// Usage unique.
	if w := h.useLink(token, "carol", testPassword); w.Code != http.StatusNotFound {
		t.Fatalf("second usage : %d", w.Code)
	}

	// Invitation admin, puis lien expiré.
	token = h.newLink(admin, "/admin/invites", url.Values{"admin": {"on"}})
	h.useLink(token, "carol", testPassword)
	if !isAdmin(t, h.db, "carol") {
		t.Fatal("invitation admin")
	}
	token = h.newLink(admin, "/admin/invites", url.Values{})
	advance := freezeTime(t)
	advance(inviteTTL + time.Minute)
	if w := h.useLink(token, "dave", testPassword); w.Code != http.StatusNotFound {
		t.Fatalf("lien expiré : %d", w.Code)
	}
}

// Formulaire du lien : Origin et jeton CSRF obligatoires.
func TestInvitationCSRF(t *testing.T) {
	h := newHarness(t)
	admin := h.session(h.addUser("alice"))
	token := h.newLink(admin, "/admin/invites", url.Values{})
	v := url.Values{"csrf": {"x"}, "name": {"bob"}, "password": {testPassword}, "confirm": {testPassword}}
	req := httptest.NewRequest("POST", "/invite/"+token, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(&http.Cookie{Name: loginCSRFCookie, Value: "y"})
	if w := h.serve(req); w.Code != http.StatusForbidden {
		t.Fatalf("%d", w.Code)
	}
	if w := h.useLink(token, "bob", testPassword); w.Code != http.StatusSeeOther {
		t.Fatal("le lien doit rester valable après un envoi refusé")
	}
}

// Administration : réservée aux admins (404 sinon), protégée contre le CSRF.
func TestAdminAccess(t *testing.T) {
	h := newHarness(t)
	admin := h.session(h.addUser("alice"))
	bob := h.session(h.addUser("bob"))
	if w := h.do(bob, "GET", "/admin", nil); w.Code != http.StatusNotFound {
		t.Fatalf("non admin : %d", w.Code)
	}
	if w := h.form(bob, "/admin/invites", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("non admin, POST : %d", w.Code)
	}
	if w := h.do(admin, "GET", "/admin", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "bob") {
		t.Fatalf("admin : %d", w.Code)
	}
	// Sans jeton CSRF : refus.
	req := httptest.NewRequest("POST", "/admin/invites", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: admin.Token})
	if w := h.serve(req); w.Code != http.StatusForbidden {
		t.Fatalf("sans CSRF : %d", w.Code)
	}
	// Lien de l'admin dans le pied de page de l'appli : seulement pour un admin.
	if !strings.Contains(h.do(admin, "GET", "/", nil).Body.String(), `href="/admin"`) ||
		strings.Contains(h.do(bob, "GET", "/", nil).Body.String(), `href="/admin"`) {
		t.Fatal("lien d'administration")
	}
}

func TestAdminResetLink(t *testing.T) {
	h := newHarness(t)
	admin := h.session(h.addUser("alice"))
	bobUser := h.addUser("bob")
	bob := h.session(bobUser)
	token := h.newLink(admin, "/admin/users/"+bobUser.ID+"/reset", url.Values{})
	h.serve(httptest.NewRequest("GET", "/invite/"+token, nil))
	w := h.useLink(token, "", newPassword)
	if w.Code != http.StatusSeeOther || sessionFrom(w) == "" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	h.expect(http.StatusUnauthorized, bob, "GET", "/api/state", nil) // anciennes sessions révoquées
	if res := h.loginForm("198.51.100.10", "bob", testPassword); res.Code == http.StatusSeeOther {
		t.Fatal("ancien mot de passe accepté")
	}
	if res := h.loginForm("198.51.100.11", "bob", newPassword); res.Code != http.StatusSeeOther {
		t.Fatal("nouveau mot de passe refusé")
	}
}

func TestAccountPassword(t *testing.T) {
	h := newHarness(t)
	u := h.addUser("alice")
	s, other := h.session(u), h.session(u)
	change := func(current, next, confirm string) int {
		return h.form(s, "/account/password", url.Values{"current": {current}, "password": {next}, "confirm": {confirm}}).Code
	}
	if c := change(testPassword, newPassword, newPassword+"x"); c != http.StatusBadRequest {
		t.Fatalf("confirmation différente : %d", c)
	}
	if c := change("mauvais", newPassword, newPassword); c != http.StatusUnauthorized {
		t.Fatalf("mot de passe actuel faux : %d", c)
	}
	w := h.form(s, "/account/password", url.Values{"current": {testPassword}, "password": {newPassword}, "confirm": {newPassword}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("changement : %d", w.Code)
	}
	// Toutes les anciennes sessions tombent, cet appareil reçoit un nouveau cookie.
	h.expect(http.StatusUnauthorized, s, "GET", "/api/state", nil)
	h.expect(http.StatusUnauthorized, other, "GET", "/api/state", nil)
	tok := sessionFrom(w)
	if tok == "" || tok == s.Token {
		t.Fatal("pas de nouvelle session pour cet appareil")
	}
	h.expect(http.StatusOK, &testSession{Token: tok, User: u}, "GET", "/api/state", nil)
	if res := h.loginForm("198.51.100.12", "alice", newPassword); res.Code != http.StatusSeeOther {
		t.Fatal("nouveau mot de passe refusé")
	}
}

func TestAccountSessions(t *testing.T) {
	h := newHarness(t)
	u := h.addUser("alice")
	s, other, third := h.session(u), h.session(u), h.session(u)
	bob := h.session(h.addUser("bob"))
	page := h.do(s, "GET", "/account", nil).Body.String()
	ids := regexp.MustCompile(`/account/sessions/([0-9a-f]{64})/revoke`).FindAllStringSubmatch(page, -1)
	if len(ids) != 2 {
		t.Fatalf("%d sessions révocables (attendu : 2, sans la courante)", len(ids))
	}
	// Session d'un autre compte : introuvable.
	if w := h.form(bob, "/account/sessions/"+ids[0][1]+"/revoke", url.Values{}); w.Code != http.StatusNotFound {
		t.Fatalf("session d'un autre : %d", w.Code)
	}
	if w := h.form(s, "/account/sessions/"+ids[0][1]+"/revoke", url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("révocation : %d", w.Code)
	}
	alive := 0
	for _, x := range []*testSession{other, third} {
		if h.do(x, "GET", "/api/state", nil).Code == http.StatusOK {
			alive++
		}
	}
	if alive != 1 {
		t.Fatalf("%d sessions encore valides", alive)
	}
	h.form(s, "/account/sessions/revoke-others", url.Values{})
	h.expect(http.StatusUnauthorized, other, "GET", "/api/state", nil)
	h.expect(http.StatusUnauthorized, third, "GET", "/api/state", nil)
	h.expect(http.StatusOK, s, "GET", "/api/state", nil)
	h.expect(http.StatusOK, bob, "GET", "/api/state", nil)
}

func TestLastAdmin(t *testing.T) {
	h := newHarness(t)
	aliceUser := h.addUser("alice")
	admin := h.session(aliceUser)
	bobUser := h.addUser("bob")
	if w := h.form(admin, "/admin/users/"+aliceUser.ID+"/role", url.Values{"admin": {"0"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("retrait du dernier admin : %d", w.Code)
	}
	if w := h.form(admin, "/admin/users/"+aliceUser.ID+"/delete", url.Values{}); w.Code != http.StatusBadRequest {
		t.Fatalf("suppression de son propre compte : %d", w.Code)
	}
	h.form(admin, "/admin/users/"+bobUser.ID+"/role", url.Values{"admin": {"1"}})
	if !isAdmin(t, h.db, "bob") {
		t.Fatal("promotion")
	}
	if w := h.form(admin, "/admin/users/"+aliceUser.ID+"/role", url.Values{"admin": {"0"}}); w.Code != http.StatusSeeOther {
		t.Fatalf("retrait avec un autre admin : %d", w.Code)
	}
	h.expect(http.StatusNotFound, admin, "GET", "/admin", nil)
	var out bytes.Buffer
	if err := adminCommand(&out, nil, h.db, []string{"user", "admin", "bob", "--revoke"}); err == nil {
		t.Fatal("CLI : retrait du dernier admin")
	}
	if err := adminCommand(&out, nil, h.db, []string{"user", "admin", "alice"}); err != nil || !isAdmin(t, h.db, "alice") {
		t.Fatal("CLI : promotion")
	}
	// Faute de frappe : refus, pas de promotion.
	for _, args := range [][]string{{"user", "admin", "carol", "--revok"}, {"user", "admin", "bob", "-r"}, {"user", "admin", "bob", "--revoke", "x"}} {
		if err := adminCommand(&out, nil, h.db, args); err == nil {
			t.Fatalf("CLI %v accepté", args)
		}
	}
}

// Deux retraits simultanés ne laissent jamais l'instance sans admin.
func TestLastAdminConcurrent(t *testing.T) {
	h := newHarness(t)
	alice, bob := h.addUser("alice"), h.addUser("bob")
	for i := 0; i < 50; i++ {
		h.db.Exec(`UPDATE users SET is_admin = 1`)
		var wg sync.WaitGroup
		for _, id := range []string{alice.ID, bob.ID} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				inTx(h.db, func(tx *sql.Tx) error { return setAdmin(tx, id, false) })
			}()
		}
		wg.Wait()
		var n int
		h.db.QueryRow(`SELECT count(*) FROM users WHERE is_admin = 1`).Scan(&n)
		if n != 1 {
			t.Fatalf("essai %d : %d admin(s)", i, n)
		}
	}
}

// Un admin qui perd son rôle, ou son compte, perd aussi ses liens en attente.
func TestDemotedAdminLinks(t *testing.T) {
	h := newHarness(t)
	root := h.session(h.addUser("root"))
	malloryUser := h.addUser("mallory")
	h.form(root, "/admin/users/"+malloryUser.ID+"/role", url.Values{"admin": {"1"}})
	mallory := h.session(malloryUser)
	invite := h.newLink(mallory, "/admin/invites", url.Values{"admin": {"on"}})
	h.form(root, "/admin/users/"+malloryUser.ID+"/role", url.Values{"admin": {"0"}})
	if w := h.useLink(invite, "mallory2", testPassword); w.Code != http.StatusNotFound {
		t.Fatalf("lien d'un admin rétrogradé : %d", w.Code)
	}

	h.form(root, "/admin/users/"+malloryUser.ID+"/role", url.Values{"admin": {"1"}})
	invite = h.newLink(mallory, "/admin/invites", url.Values{"admin": {"on"}})
	h.form(root, "/admin/users/"+malloryUser.ID+"/delete", url.Values{})
	if w := h.useLink(invite, "mallory3", testPassword); w.Code != http.StatusNotFound {
		t.Fatalf("lien d'un compte supprimé : %d", w.Code)
	}
}

// Premier compte créé en ligne de commande : le lien écrit dans les journaux ne sert plus.
func TestSetupLinkAfterCLI(t *testing.T) {
	h := newHarness(t)
	token, _ := setupInvitation(h.db)
	var out bytes.Buffer
	if err := adminCommand(&out, strings.NewReader(testPassword+"\n"), h.db, []string{"user", "add", "alice", "--password-stdin"}); err != nil {
		t.Fatal(err)
	}
	if w := h.useLink(token, "eve", testPassword); w.Code != http.StatusNotFound {
		t.Fatalf("lien de premier compte encore valable : %d", w.Code)
	}
}

// Formulaire d'un lien envoyé avec un cookie de formulaire périmé : la page, pas du JSON.
func TestInviteExpiredForm(t *testing.T) {
	h := newHarness(t)
	admin := h.session(h.addUser("alice"))
	token := h.newLink(admin, "/admin/invites", url.Values{})
	form := url.Values{"csrf": {"périmé"}, "name": {"bob"}, "password": {testPassword}, "confirm": {testPassword}}
	r := httptest.NewRequest("POST", "/invite/"+token, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", testOrigin)
	w := h.serve(r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "<form") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if w := h.useLink(token, "bob", testPassword); w.Code != http.StatusSeeOther {
		t.Fatalf("lien consommé par un envoi refusé : %d", w.Code)
	}
}

// Suppression d'un compte : ses listes partagées disparaissent chez les autres, ses
// sessions sont révoquées.
func TestAdminDeleteUser(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	bobUser := h.addUser("bob")
	bob := h.session(bobUser)
	var l List
	h.mustDo(bob, "POST", "/api/lists", map[string]any{"name": "Courses", "color": colors[0].Key, "shared": true}, &l)
	if !hasList(h.state(alice), l.ID) {
		t.Fatal("liste partagée invisible")
	}
	if w := h.form(alice, "/admin/users/"+bobUser.ID+"/delete", url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if hasList(h.state(alice), l.ID) {
		t.Fatal("liste d'un compte supprimé encore visible")
	}
	h.expect(http.StatusUnauthorized, bob, "GET", "/api/state", nil)
	if _, err := userByName(h.db, "bob"); err == nil {
		t.Fatal("compte encore là")
	}
}

func TestAdminSignOut(t *testing.T) {
	h := newHarness(t)
	admin := h.session(h.addUser("alice"))
	bobUser := h.addUser("bob")
	bob := h.session(bobUser)
	h.form(admin, "/admin/users/"+bobUser.ID+"/sign-out", url.Values{})
	h.expect(http.StatusUnauthorized, bob, "GET", "/api/state", nil)
	h.expect(http.StatusOK, admin, "GET", "/api/state", nil)
}

func TestInviteCLI(t *testing.T) {
	useLang(t, "en")
	h := newHarness(t)
	var out bytes.Buffer
	if err := inviteCommand(&out, h.db, testOrigin, true); err != nil {
		t.Fatal(err)
	}
	m := linkRe.FindStringSubmatch(out.String())
	if m == nil || !strings.Contains(out.String(), testOrigin+"/invite/") {
		t.Fatalf("sortie : %s", out.String())
	}
	if w := h.useLink(m[1], "alice", testPassword); w.Code != http.StatusSeeOther || !isAdmin(t, h.db, "alice") {
		t.Fatalf("%d", w.Code)
	}
}

func TestDeviceName(t *testing.T) {
	useLang(t, "en")
	for ua, want := range map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": "Safari on iPhone",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:140.0) Gecko/20100101 Firefox/140.0":                                                    "Firefox on macOS",
		"Mozilla/5.0 (Linux; Android 15) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Mobile Safari/537.36":                                "Chrome on Android",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Edg/140.0":                   "Edge on Windows",
		"curl/8.0": "Unknown device",
		"":         "Unknown device",
	} {
		if got := deviceName(ua); got != want {
			t.Errorf("%q : %q, attendu %q", ua, got, want)
		}
	}
}
