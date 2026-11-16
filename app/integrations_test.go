package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// Dupliquer : copie privée à soi, éléments décochés, catégories gardées ; idempotent.
func TestDuplicateList(t *testing.T) {
	useLang(t, "en")
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	bobUser := h.addUser("bob")
	bob, carol := h.session(bobUser), h.session(h.addUser("carol"))
	l := h.createList(alice, "Packing")
	var cat Item
	h.mustDo(alice, "POST", "/api/lists/"+l.ID+"/items", map[string]any{"text": "Clothes", "category": true}, &cat)
	sock := h.createItem(alice, l.ID, "Socks")
	h.createItem(alice, l.ID, "Shirt")
	h.expect(http.StatusOK, alice, "PATCH", "/api/items/"+sock.ID, map[string]any{"checked": true})
	h.expect(http.StatusOK, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID}})

	h.expect(http.StatusNotFound, carol, "POST", "/api/lists/"+l.ID+"/duplicate", map[string]any{"nonce": newID()})
	nonce := newID()
	var cp List
	h.mustDo(bob, "POST", "/api/lists/"+l.ID+"/duplicate", map[string]any{"nonce": nonce}, &cp)
	if cp.OwnerID != bobUser.ID || cp.Name != "Packing (copy)" || cp.Shared || len(cp.Members) != 0 || len(cp.Items) != 3 {
		t.Fatalf("copie : %+v", cp)
	}
	if !cp.Items[0].Category || cp.Items[0].Text != "Clothes" || cp.Items[1].Checked || cp.Items[1].Text != "Socks" {
		t.Fatalf("éléments : %+v", cp.Items)
	}
	var again List
	h.mustDo(bob, "POST", "/api/lists/"+l.ID+"/duplicate", map[string]any{"nonce": nonce}, &again)
	if again.ID != cp.ID || countLists(h.state(bob)) != 2 {
		t.Fatal("renvoi : doublon")
	}
	if hasList(h.state(alice), cp.ID) {
		t.Fatal("copie visible par le propriétaire de l'original")
	}
}

func countLists(st stateResponse) int { return len(st.Lists) }

// importForm : envoi d'un fichier par le formulaire de « Mon compte ».
func (h *harness) importForm(s *testSession, content string) *httptest.ResponseRecorder {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("csrf", s.CSRF)
	fw, _ := mw.CreateFormFile("file", "lists.json")
	fw.Write([]byte(content))
	mw.Close()
	req := httptest.NewRequest("POST", "/account/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.Token})
	return h.serve(req)
}

// Export puis import : les listes à soi seulement, recréées à neuf (privées, jamais fusionnées).
func TestExportImport(t *testing.T) {
	useLang(t, "en")
	h := newHarness(t)
	aliceUser := h.addUser("alice")
	alice, bob := h.session(aliceUser), h.session(h.addUser("bob"))
	l := h.createList(alice, "Groceries")
	h.mustDo(alice, "POST", "/api/lists/"+l.ID+"/items", map[string]any{"text": "Fruit", "category": true}, nil)
	milk := h.createItem(alice, l.ID, "Milk")
	h.expect(http.StatusOK, alice, "PATCH", "/api/items/"+milk.ID, map[string]any{"checked": true})
	h.expect(http.StatusOK, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": true})
	bobs := h.createList(bob, "Bob's") // partagée ou non, la liste d'un autre n'est pas exportée
	h.expect(http.StatusOK, bob, "PATCH", "/api/lists/"+bobs.ID, map[string]any{"shared": true})

	w := h.do(alice, "GET", "/account/export", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export : %d %v", w.Code, w.Header())
	}
	var f exportFile
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil || f.Format != exportFormat || len(f.Lists) != 1 {
		t.Fatalf("export : %v %s", err, w.Body.String())
	}
	if got := f.Lists[0]; got.Name != "Groceries" || len(got.Items) != 2 || !got.Items[0].Category || !got.Items[1].Checked {
		t.Fatalf("liste exportée : %+v", got)
	}

	carol := h.session(h.addUser("carol"))
	if w := h.importForm(carol, w.Body.String()); w.Code != http.StatusSeeOther {
		t.Fatalf("import : %d %s", w.Code, w.Body.String())
	}
	st := h.state(carol)
	if len(st.Lists) != 3 { // la sienne, importée, et les deux partagées avec tous
		t.Fatalf("listes après import : %d", len(st.Lists))
	}
	var imported *List
	for _, x := range st.Lists {
		if x.OwnerID == carol.User.ID {
			imported = x
		}
	}
	if imported == nil || imported.Name != "Groceries" || imported.Shared || len(imported.Items) != 2 || !imported.Items[1].Checked {
		t.Fatalf("liste importée : %+v", imported)
	}

	for _, bad := range []string{`{"format":"autre","version":1,"lists":[]}`, `pas du json`, `{"format":"giga-simple-lists","version":1,"lists":[],"x":1}`,
		`{"format":"giga-simple-lists","version":1,"lists":[]} {}`} {
		if w := h.importForm(carol, bad); w.Code != http.StatusBadRequest {
			t.Fatalf("import %q : %d", bad, w.Code)
		}
	}
	if w := h.importForm(carol, strings.Repeat(" ", maxImportBytes+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("import trop gros : %d", w.Code)
	}
	// Nom de compte accentué : nom de fichier encodé (RFC 2231).
	zoe := h.session(h.addUser("zoé"))
	if cd := h.do(zoe, "GET", "/account/export", nil).Header().Get("Content-Disposition"); !strings.Contains(cd, "filename*=utf-8''giga-simple-lists-zo%C3%A9-") {
		t.Fatalf("nom de fichier : %s", cd)
	}
	// Sans jeton CSRF : refusé.
	req := httptest.NewRequest("POST", "/account/import", strings.NewReader(""))
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: carol.Token})
	if w := h.serve(req); w.Code != http.StatusForbidden {
		t.Fatalf("import sans CSRF : %d", w.Code)
	}
}

var tokenRe = regexp.MustCompile(`value="(gsl_[A-Za-z0-9_-]+)"`)

func bearer(h *harness, token, method, path string, body any) *httptest.ResponseRecorder {
	req := h.req(nil, method, path, body)
	req.Header.Del("Origin")
	req.Header.Del("Sec-Fetch-Site")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return h.serve(req)
}

// Jeton d'API : créé dans « Mon compte », affiché une fois ; ajoute un élément à une liste
// visible, par son nom ; révoqué ou remplacé, il ne marche plus.
func TestAPIToken(t *testing.T) {
	useLang(t, "en")
	h := newHarness(t)
	alice, bob := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	l := h.createList(alice, "Groceries")
	private := h.createList(bob, "Secret")

	w := h.form(alice, "/account/token", url.Values{})
	m := tokenRe.FindStringSubmatch(w.Body.String())
	if w.Code != http.StatusOK || m == nil {
		t.Fatalf("création : %d", w.Code)
	}
	token := m[1]
	if strings.Contains(h.do(alice, "GET", "/account", nil).Body.String(), token) {
		t.Fatal("jeton réaffiché")
	}

	var it Item
	w = bearer(h, token, "POST", "/api/v1/items", map[string]any{"list": "groceries", "text": "Milk"})
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &it) != nil || it.ListID != l.ID || it.Text != "Milk" {
		t.Fatalf("ajout : %d %s", w.Code, w.Body.String())
	}
	if c := bearer(h, token, "POST", "/api/v1/items", map[string]any{"list": l.ID, "text": "Bread"}).Code; c != http.StatusCreated {
		t.Fatalf("par identifiant : %d", c)
	}
	if c := bearer(h, token, "POST", "/api/v1/items", map[string]any{"list": "Secret", "text": "x"}).Code; c != http.StatusNotFound {
		t.Fatalf("liste d'un autre, par nom : %d", c)
	}
	if c := bearer(h, token, "POST", "/api/v1/items", map[string]any{"list": private.ID, "text": "x"}).Code; c != http.StatusNotFound {
		t.Fatalf("liste d'un autre, par identifiant : %d", c)
	}
	var lists []apiListSummary
	if w := bearer(h, token, "GET", "/api/v1/lists", nil); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &lists) != nil || len(lists) != 1 || lists[0].Items != 2 {
		t.Fatalf("listes : %d %s", w.Code, w.Body.String())
	}

	// Le jeton ne sert qu'aux routes /api/v1 ; la session ne sert pas à /api/v1.
	if c := bearer(h, token, "GET", "/api/state", nil).Code; c != http.StatusUnauthorized {
		t.Fatalf("jeton sur l'API de l'appli : %d", c)
	}
	if c := h.do(alice, "GET", "/api/v1/lists", nil).Code; c != http.StatusUnauthorized {
		t.Fatalf("cookie sur /api/v1 : %d", c)
	}
	for _, bad := range []string{"", "gsl_faux", token + "x"} {
		if c := bearer(h, bad, "GET", "/api/v1/lists", nil).Code; c != http.StatusUnauthorized {
			t.Fatalf("jeton %q : %d", bad, c)
		}
	}

	// Schéma insensible à la casse (RFC 6750) ; nonce : une nouvelle tentative n'ajoute rien.
	low := h.req(nil, "GET", "/api/v1/lists", nil)
	low.Header.Set("Authorization", "bearer "+token)
	if c := h.serve(low).Code; c != http.StatusOK {
		t.Fatalf("bearer en minuscules : %d", c)
	}
	nonce := strings.Repeat("ab", 16)
	var first, again Item
	for _, out := range []*Item{&first, &again} {
		w := bearer(h, token, "POST", "/api/v1/items", map[string]any{"list": l.ID, "text": "Eggs", "nonce": nonce})
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), out) != nil {
			t.Fatalf("avec nonce : %d %s", w.Code, w.Body.String())
		}
	}
	if first.ID != again.ID || len(h.state(alice).Lists[0].Items) != 3 {
		t.Fatalf("nonce rejoué : %s %s", first.ID, again.ID)
	}
	// Nom porté par plusieurs listes partagées d'autres comptes : ambigu.
	carol := h.session(h.addUser("carol"))
	for _, s := range []*testSession{bob, carol} {
		sl := h.createList(s, "Hardware")
		h.expect(http.StatusOK, s, "PATCH", "/api/lists/"+sl.ID, map[string]any{"shared": true})
	}
	if c := bearer(h, token, "POST", "/api/v1/items", map[string]any{"list": "hardware", "text": "x"}).Code; c != http.StatusConflict {
		t.Fatalf("nom ambigu : %d", c)
	}

	// Un compte compromis ne garde pas son jeton : changement de mot de passe, déconnexion par l'admin.
	newToken := func(s *testSession) string {
		m := tokenRe.FindStringSubmatch(h.form(s, "/account/token", url.Values{}).Body.String())
		if m == nil {
			t.Fatal("pas de jeton")
		}
		return m[1]
	}
	bt := newToken(bob)
	h.form(alice, "/admin/users/"+bob.User.ID+"/sign-out", url.Values{})
	if c := bearer(h, bt, "GET", "/api/v1/lists", nil).Code; c != http.StatusUnauthorized {
		t.Fatalf("jeton après « Déconnecter partout » : %d", c)
	}
	h.expect(http.StatusUnauthorized, bob, "GET", "/api/state", nil)
	h.expect(http.StatusOK, alice, "GET", "/api/state", nil)
	bob = h.session(bob.User)
	bt = newToken(bob)
	pw := "another-long-password-2026"
	if w := h.form(bob, "/account/password", url.Values{"current": {testPassword}, "password": {pw}, "confirm": {pw}}); w.Code >= 400 {
		t.Fatalf("mot de passe : %d", w.Code)
	}
	if c := bearer(h, bt, "GET", "/api/v1/lists", nil).Code; c != http.StatusUnauthorized {
		t.Fatalf("jeton après changement de mot de passe : %d", c)
	}

	h.form(alice, "/account/token", url.Values{}) // remplacé
	if c := bearer(h, token, "GET", "/api/v1/lists", nil).Code; c != http.StatusUnauthorized {
		t.Fatalf("ancien jeton : %d", c)
	}
	h.form(alice, "/account/token/revoke", url.Values{})
	var n int
	h.db.QueryRow(`SELECT count(*) FROM api_tokens`).Scan(&n)
	if n != 0 {
		t.Fatal("jeton non révoqué")
	}
}

// En-tête du reverse proxy : cru seulement depuis un relais de confiance, pour un compte existant.
func TestAuthHeader(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	page := func(remote, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = remote + ":5555"
		if user != "" {
			req.Header.Set("Remote-User", user)
		}
		return h.serve(req)
	}
	if c := page("127.0.0.1", "alice").Code; c != http.StatusSeeOther {
		t.Fatalf("désactivé par défaut : %d", c)
	}
	h.app.cfg.AuthHeader = "Remote-User"
	w := page("127.0.0.1", "Alice")
	if w.Code != http.StatusOK || sessionFrom(w) == "" {
		t.Fatalf("relais de confiance : %d", w.Code)
	}
	if c := page("203.0.113.9", "alice").Code; c != http.StatusSeeOther {
		t.Fatalf("relais inconnu : %d", c)
	}
	if c := page("127.0.0.1", "mallory").Code; c != http.StatusSeeOther {
		t.Fatalf("compte inconnu : %d", c)
	}
	if c := page("127.0.0.1", "").Code; c != http.StatusSeeOther {
		t.Fatalf("sans en-tête : %d", c)
	}
	// /login, déjà authentifié par le proxy : directement l'appli.
	req := httptest.NewRequest("GET", "/login", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Remote-User", "alice")
	if w := h.serve(req); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("/login : %d %s", w.Code, w.Header().Get("Location"))
	}

	// Requête d'une page déjà ouverte (cookie, jeton CSRF en mémoire), via le proxy.
	open := func(method, path, cookie, csrf, user string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "127.0.0.1:5555"
		req.Header.Set("Remote-User", user)
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("X-CSRF-Token", csrf)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookie})
		return h.serve(req)
	}
	tok := sessionFrom(w)
	s, _, err := lookupSession(h.db, tok)
	if err != nil {
		t.Fatal(err)
	}
	// /login avec la session du même compte : gardée (les onglets ouverts continuent).
	if w := open("GET", "/login", tok, "", "alice"); w.Code != http.StatusSeeOther || sessionFrom(w) != "" {
		t.Fatalf("/login, session gardée : %d %q", w.Code, sessionFrom(w))
	}
	if c := open("POST", "/api/lists", tok, s.CSRF, "alice").Code; c == http.StatusUnauthorized || c == http.StatusForbidden {
		t.Fatalf("écriture après /login : %d", c)
	}
	// Session révoquée : l'API répond 401 (l'appli repasse par /login et recharge la page)
	// au lieu d'ouvrir une session dont la page n'a pas le jeton CSRF (403, écritures perdues).
	deleteSession(h.db, s.Hash)
	for _, m := range []string{"GET /api/events", "GET /api/state", "POST /api/lists"} {
		method, path, _ := strings.Cut(m, " ")
		if w := open(method, path, tok, s.CSRF, "alice"); w.Code != http.StatusUnauthorized || sessionFrom(w) != "" {
			t.Fatalf("%s, session révoquée : %d %q", m, w.Code, sessionFrom(w))
		}
	}
	// Autre compte derrière le proxy (cookie d'alice, en-tête de bob) : jamais servi en alice.
	h.addUser("bob")
	tok = sessionFrom(page("127.0.0.1", "alice"))
	if c := open("GET", "/api/state", tok, "", "bob").Code; c != http.StatusUnauthorized {
		t.Fatalf("API, autre compte : %d", c)
	}
	w = open("GET", "/login", tok, "", "bob")
	nt := sessionFrom(w)
	if ns, _, err := lookupSession(h.db, nt); w.Code != http.StatusSeeOther || err != nil || ns.UserName != "bob" {
		t.Fatalf("/login, autre compte : %d %v", w.Code, err)
	}
	if _, _, err := lookupSession(h.db, tok); err == nil {
		t.Fatal("session d'alice gardée")
	}
}
