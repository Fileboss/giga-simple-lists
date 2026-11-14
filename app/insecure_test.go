package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	useLang(t, "en")
	for _, c := range []struct {
		origin   string
		insecure bool
		ok       bool
	}{
		{"https://lists.example.com", false, true},
		{"https://lists.example.com", true, false}, // l'option retirerait Secure à une instance HTTPS
		{"http://localhost:5240", false, true},
		{"http://localhost:5240", true, true},
		{"http://192.168.1.10:5240", false, false}, // la connexion échouerait : on refuse de démarrer
		{"http://192.168.1.10:5240", true, true},
		{"http://lists.lan", true, true},
		{"", false, false},
		{"", true, false},
		{"http://", true, false},
		{"ftp://lists.example.com", true, false},
	} {
		err := checkOrigin(config{Origin: c.origin, InsecureHTTP: c.insecure})
		if (err == nil) != c.ok {
			t.Errorf("%q insecure=%v : %v", c.origin, c.insecure, err)
		}
	}
}

// Sans LISTES_INSECURE_HTTP, les cookies ne changent pas : __Host-, Secure.
func TestCookiesSecureByDefault(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	res := h.loginForm("198.51.100.1", "alice", testPassword)
	if res.Code != http.StatusSeeOther || res.Cookie == nil {
		t.Fatalf("connexion : %d %s", res.Code, res.Body)
	}
	if res.Cookie.Name != "__Host-session" || !res.Cookie.Secure || !res.Cookie.HttpOnly || res.Cookie.Path != "/" {
		t.Fatalf("cookie : %+v", res.Cookie)
	}
}

// Avec LISTES_INSECURE_HTTP sur une origine http://, toute la boucle fonctionne : cookies
// sans Secure ni préfixe __Host- (refusés en HTTP simple), mêmes défenses CSRF.
func TestInsecureHTTPLogin(t *testing.T) {
	const origin = "http://192.168.1.10:5240"
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "listes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app := newApp(config{Addr: ":0", Origin: origin, TrustedProxies: []string{"127.0.0.1"}, InsecureHTTP: true}, db)
	app.loginMin = 0
	h := &harness{t: t, app: app, db: db, dir: dir}
	h.addUser("alice")

	w := h.serve(httptest.NewRequest("GET", "/login", nil))
	var csrf *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "listes_login" {
			csrf = c
		}
	}
	if csrf == nil || csrf.Secure {
		t.Fatalf("cookie de connexion : %v", w.Result().Cookies())
	}
	post := func(origin string) *httptest.ResponseRecorder {
		form := url.Values{"csrf": {csrf.Value}, "name": {"alice"}, "password": {testPassword}}
		req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", origin)
		req.AddCookie(csrf)
		return h.serve(req)
	}
	if w := post("http://evil.example"); w.Code != http.StatusForbidden {
		t.Fatalf("autre origine : %d", w.Code)
	}
	w = post(origin)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("connexion : %d %s", w.Code, w.Body.String())
	}
	var sess *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "listes_session" {
			sess = c
		}
	}
	if sess == nil || sess.Secure || !sess.HttpOnly {
		t.Fatalf("cookie de session : %v", w.Result().Cookies())
	}

	req := httptest.NewRequest("GET", "/api/state", nil)
	req.AddCookie(sess)
	if w := h.serve(req); w.Code != http.StatusOK {
		t.Fatalf("état : %d", w.Code)
	}
	// Le cookie sécurisé n'est pas lu dans ce mode (et inversement).
	req = httptest.NewRequest("GET", "/api/state", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: sess.Value})
	if w := h.serve(req); w.Code != http.StatusUnauthorized {
		t.Fatalf("__Host-session accepté : %d", w.Code)
	}
}

// Formulaire envoyé depuis une autre adresse que LISTES_ORIGIN (http://<ip>:5240 au lieu
// de l'URL publique) : le message le dit, au lieu de « page expirée ».
func TestLoginWrongOriginMessage(t *testing.T) {
	useLang(t, "en")
	h := newHarness(t)
	h.addUser("alice")
	form := url.Values{"csrf": {"x"}, "name": {"alice"}, "password": {testPassword}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://192.168.1.10:5240")
	w := h.serve(req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), testOrigin) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// Bonne origine, jeton expiré : message habituel.
	req = httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	w = h.serve(req)
	if !strings.Contains(w.Body.String(), T("login.expired")) {
		t.Fatalf("%s", w.Body.String())
	}
}

func TestBackup(t *testing.T) {
	h := newHarness(t)
	a := h.session(h.addUser("alice"))
	var l List
	h.mustDo(a, "POST", "/api/lists", map[string]any{"nonce": newID(), "name": "Courses", "color": colors[0].Key}, &l)

	var out bytes.Buffer
	dbPath := filepath.Join(h.dir, "listes.db")
	if err := backup(&out, h.db, dbPath, ""); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(h.dir, "listes-backup-*.db"))
	if len(matches) != 1 || !strings.Contains(out.String(), matches[0]) {
		t.Fatalf("copie : %v, sortie %q", matches, out.String())
	}
	// La copie est une base complète, ouvrable telle quelle.
	cp, err := openDB(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var name string
	if err := cp.QueryRow(`SELECT name FROM lists WHERE id = ?`, l.ID).Scan(&name); err != nil || name != "Courses" {
		t.Fatalf("copie : %q %v", name, err)
	}
	// Jamais d'écrasement.
	dest := filepath.Join(h.dir, "existant.db")
	os.WriteFile(dest, []byte("à garder"), 0o600)
	if err := backup(&out, h.db, dbPath, dest); err == nil {
		t.Fatal("fichier existant écrasé")
	}
	if b, _ := os.ReadFile(dest); string(b) != "à garder" {
		t.Fatal("fichier existant modifié")
	}
}
