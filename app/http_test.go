package main

import (
	"database/sql"
	"fmt"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CSRF

func TestCSRFOriginAndToken(t *testing.T) {
	h := newHarness(t)
	s := h.session(h.addUser("alice"))
	body := map[string]any{"name": "x"}
	cases := []struct {
		name  string
		tweak func(*http.Request)
		code  int
	}{
		{"requête légitime", func(*http.Request) {}, http.StatusOK},
		{"Origin absent", func(r *http.Request) { r.Header.Del("Origin") }, http.StatusForbidden},
		{"Origin étranger", func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"Origin null", func(r *http.Request) { r.Header.Set("Origin", "null") }, http.StatusForbidden},
		{"Origin sous-domaine", func(r *http.Request) { r.Header.Set("Origin", "https://x.listes.test") }, http.StatusForbidden},
		{"Sec-Fetch-Site cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		{"Sec-Fetch-Site same-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, http.StatusForbidden},
		{"Sec-Fetch-Site absent (ancien navigateur)", func(r *http.Request) { r.Header.Del("Sec-Fetch-Site") }, http.StatusOK},
		{"jeton absent", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, http.StatusForbidden},
		{"jeton faux", func(r *http.Request) { r.Header.Set("X-CSRF-Token", "faux") }, http.StatusForbidden},
	}
	for _, c := range cases {
		req := h.req(s, "POST", "/api/lists", body)
		c.tweak(req)
		if w := h.serve(req); w.Code != c.code {
			t.Errorf("%s : attendu %d, obtenu %d", c.name, c.code, w.Code)
		}
	}
	// Le jeton d'une autre session ne vaut rien.
	other := h.session(h.addUser("bob"))
	req := h.req(s, "POST", "/api/lists", body)
	req.Header.Set("X-CSRF-Token", other.CSRF)
	if w := h.serve(req); w.Code != http.StatusForbidden {
		t.Errorf("jeton d'une autre session accepté : %d", w.Code)
	}
}

func TestCSRFLogin(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	post := func(tweak func(*http.Request)) int {
		w := h.serve(httptest.NewRequest("GET", "/login", nil))
		c := w.Result().Cookies()[0]
		form := url.Values{"csrf": {c.Value}, "name": {"alice"}, "password": {testPassword}}
		req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", testOrigin)
		req.AddCookie(c)
		tweak(req)
		return h.serve(req).Code
	}
	if code := post(func(r *http.Request) { r.Header.Del("Origin") }); code != http.StatusForbidden {
		t.Fatalf("Origin absent : %d", code)
	}
	if code := post(func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); code != http.StatusForbidden {
		t.Fatalf("Origin étranger : %d", code)
	}
	if code := post(func(r *http.Request) { r.Header.Del("Cookie") }); code != http.StatusForbidden {
		t.Fatalf("cookie CSRF absent : %d", code)
	}
}

// ---------------------------------------------------------------------------
// IP du client

func TestClientIP(t *testing.T) {
	trusted := []string{"127.0.0.1", "172.29.52.1"}
	cases := []struct {
		remote, xff, want string
	}{
		{"127.0.0.1:1234", "198.51.100.7", "198.51.100.7"},
		{"127.0.0.1:1234", "10.0.0.1, 198.51.100.7", "198.51.100.7"}, // dernière valeur : celle de Caddy
		{"127.0.0.1:1234", "", "127.0.0.1"},
		{"127.0.0.1:1234", "pas-une-ip", "127.0.0.1"},
		{"127.0.0.1:1234", "2001:db8::1", "2001:db8::1"},
		{"172.29.52.1:1234", "198.51.100.7", "198.51.100.7"}, // passerelle Docker déclarée
		{"203.0.113.9:1234", "198.51.100.7", "203.0.113.9"},  // ignoré hors relais de confiance
		{"127.0.0.2:1234", "198.51.100.7", "127.0.0.2"},
		{"[::1]:1234", "198.51.100.7", "::1"},
		{"172.29.52.2:1234", "198.51.100.7", "172.29.52.2"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r, trusted); got != c.want {
			t.Errorf("%s + %q : %s, attendu %s", c.remote, c.xff, got, c.want)
		}
	}
	// Valeur par défaut : seul 127.0.0.1 est de confiance.
	t.Setenv("LISTES_TRUSTED_PROXIES", "")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.29.52.1:1"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := clientIP(r, loadConfig().TrustedProxies); got != "172.29.52.1" {
		t.Errorf("défaut : %s", got)
	}
}

// Une IP forgée dans X-Forwarded-For par un client direct ne contourne pas la limite.
func TestRateLimitIgnoresSpoofedXFF(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	for i := 0; i < 12; i++ {
		form := url.Values{"name": {fmt.Sprintf("u%d", i)}, "password": {"x"}}
		get := h.serve(httptest.NewRequest("GET", "/login", nil))
		c := get.Result().Cookies()[0]
		form.Set("csrf", c.Value)
		req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", testOrigin)
		req.AddCookie(c)
		req.RemoteAddr = "203.0.113.50:999"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i))
		h.serve(req)
	}
	blocked, _ := loginBlocked(h.db, ipKey("203.0.113.50"))
	if !blocked {
		t.Fatal("l'IP réelle aurait dû être bloquée")
	}
}

// ---------------------------------------------------------------------------
// Limites de taille

func TestSizeLimits(t *testing.T) {
	h := newHarness(t)
	s := h.session(h.addUser("alice"))
	l := h.createList(s, "L")
	items := "/api/lists/" + l.ID + "/items"

	h.expect(http.StatusOK, s, "POST", items, map[string]any{"text": strings.Repeat("é", maxItemText)})
	h.expect(http.StatusBadRequest, s, "POST", items, map[string]any{"text": strings.Repeat("é", maxItemText+1)})
	h.expect(http.StatusBadRequest, s, "POST", items, map[string]any{"text": "   "})
	it := h.createItem(s, l.ID, "ok")
	h.expect(http.StatusBadRequest, s, "PATCH", "/api/items/"+it.ID, map[string]any{"text": strings.Repeat("a", maxItemText+1)})

	h.expect(http.StatusOK, s, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": strings.Repeat("ü", maxListName)})
	h.expect(http.StatusBadRequest, s, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": strings.Repeat("ü", maxListName+1)})
	h.expect(http.StatusBadRequest, s, "POST", "/api/lists", map[string]any{"name": strings.Repeat("a", maxListName+1)})
	h.expect(http.StatusBadRequest, s, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": ""})
	h.expect(http.StatusBadRequest, s, "PATCH", "/api/lists/"+l.ID, map[string]any{"color": "#ff0000"})

	// Corps > 16 Ko.
	big := `{"text":"` + strings.Repeat("a", maxBodyBytes) + `"}`
	h.expect(http.StatusRequestEntityTooLarge, s, "POST", items, big)
	// JSON strict.
	h.expect(http.StatusBadRequest, s, "POST", items, `{"text":"a","inconnu":1}`)
	h.expect(http.StatusBadRequest, s, "POST", items, `{"text":"a"}{"text":"b"}`)
	h.expect(http.StatusBadRequest, s, "POST", items, `pas du json`)
	req := h.req(s, "POST", items, `{"text":"a"}`)
	req.Header.Set("Content-Type", "text/plain")
	if w := h.serve(req); w.Code != http.StatusBadRequest {
		t.Fatalf("Content-Type text/plain accepté : %d", w.Code)
	}
	// Caractères de contrôle neutralisés.
	var clean Item
	h.mustDo(s, "POST", items, map[string]any{"text": "a\nb\u0000c"}, &clean)
	if clean.Text != "a b c" {
		t.Fatalf("texte nettoyé : %q", clean.Text)
	}
}

func TestCountLimits(t *testing.T) {
	h := newHarness(t)
	s := h.session(h.addUser("alice"))
	l := h.createList(s, "L")
	// 2 000 éléments insérés directement, le 2 001ᵉ par l'API est refusé.
	tx, _ := h.db.Begin()
	for i := 1; i < maxItemsPerList; i++ {
		tx.Exec(`INSERT INTO items (id, list_id, text, position, updated_at) VALUES (?, ?, 'x', ?, 0)`, newID(), l.ID, i)
	}
	tx.Commit()
	h.createItem(s, l.ID, "le 2000e")
	h.expect(http.StatusConflict, s, "POST", "/api/lists/"+l.ID+"/items", map[string]any{"text": "de trop"})

	tx, _ = h.db.Begin()
	for i := 1; i < maxListsPerUser; i++ {
		tx.Exec(`INSERT INTO lists (id, owner_id, name, color, position, created_at, updated_at) VALUES (?, ?, 'x', 'jaune', ?, 0, 0)`,
			newID(), s.User.ID, i+1)
	}
	tx.Commit()
	h.expect(http.StatusConflict, s, "POST", "/api/lists", map[string]any{"name": "de trop"})
	// La limite est par utilisateur.
	other := h.session(h.addUser("bob"))
	h.createList(other, "ok")
}

// ---------------------------------------------------------------------------
// En-têtes

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t)
	s := h.session(h.addUser("alice"))
	want := map[string]string{
		"Content-Security-Policy":      "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Frame-Options":              "DENY",
	}
	for _, path := range []string{"/", "/api/state", "/login", "/static/app.js", "/nope"} {
		w := h.do(s, "GET", path, nil)
		for k, v := range want {
			if got := w.Header().Get(k); got != v {
				t.Errorf("%s %s : %q", path, k, got)
			}
		}
		pp := w.Header().Get("Permissions-Policy")
		if !strings.Contains(pp, "camera=()") || !strings.Contains(pp, "geolocation=()") || strings.Contains(pp, "self") {
			t.Errorf("%s Permissions-Policy : %q", path, pp)
		}
	}
	for _, path := range []string{"/", "/api/state", "/login"} {
		if cc := h.do(s, "GET", path, nil).Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s Cache-Control : %q", path, cc)
		}
	}
	m := h.do(nil, "GET", "/manifest.webmanifest", nil)
	if m.Code != 200 || m.Header().Get("Content-Type") != "application/manifest+json" || !strings.Contains(m.Body.String(), `"standalone"`) {
		t.Errorf("manifeste : %d %s", m.Code, m.Header().Get("Content-Type"))
	}
	// Service worker servi à la racine (portée « / »), revalidé à chaque fois.
	sw := h.do(nil, "GET", "/sw.js", nil)
	if sw.Code != 200 || !strings.HasPrefix(sw.Header().Get("Content-Type"), "text/javascript") || sw.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("sw.js : %d %s %s", sw.Code, sw.Header().Get("Content-Type"), sw.Header().Get("Cache-Control"))
	}
	// Non connecté : redirection vers /login, et l'API répond 401.
	if w := h.do(nil, "GET", "/", nil); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/login" {
		t.Errorf("/ sans session : %d", w.Code)
	}
	h.expect(http.StatusUnauthorized, nil, "GET", "/api/state", nil)
	h.expect(http.StatusOK, nil, "GET", "/healthz", nil)
}

// Aucun script ni style inline (la CSP les bloquerait), pas d'alert()/confirm(), pas d'innerHTML.
func TestNoInlineCode(t *testing.T) {
	inlineScript := regexp.MustCompile(`<script(?:\s[^>]*)?>\s*[^<\s]`)
	fs.WalkDir(templatesFS, ".", func(p string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}
		b, _ := templatesFS.ReadFile(p)
		s := string(b)
		if inlineScript.MatchString(s) || strings.Contains(s, "<style") || strings.Contains(s, "style=") ||
			regexp.MustCompile(`\son[a-z]+=`).MatchString(s) || strings.Contains(s, "javascript:") {
			t.Errorf("%s : code inline", p)
		}
		return nil
	})
	js, _ := staticFS.ReadFile("web/static/app.js")
	// Le CSSOM (el.style.transform) est permis par la CSP ; les attributs style inline non.
	for _, bad := range []string{"alert(", "confirm(", "prompt(", "innerHTML", "outerHTML", "eval(", "new Function", "document.write",
		"setAttribute('style'", "cssText", "style="} {
		if strings.Contains(string(js), bad) {
			t.Errorf("app.js contient %s", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// Couleurs : contraste du texte noir ≥ 7:1, du gris des cochés ≥ 4,5:1.

func luminance(hex string) float64 {
	v, _ := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	ch := func(c uint64) float64 {
		s := float64(c) / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(v>>16&0xff) + 0.7152*ch(v>>8&0xff) + 0.0722*ch(v&0xff)
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)
}

func TestColorContrast(t *testing.T) {
	css, _ := staticFS.ReadFile("web/static/app.css")
	if len(colors) != 8 {
		t.Fatalf("%d couleurs", len(colors))
	}
	for _, c := range colors {
		if r := contrast("#000000", c.Hex); r < 7 {
			t.Errorf("%s : texte noir %.2f:1", c.Key, r)
		}
		if r := contrast(inkColor, c.Hex); r < 7 {
			t.Errorf("%s : texte %.2f:1", c.Key, r)
		}
		if r := contrast(checkedGray, c.Hex); r < 4.5 {
			t.Errorf("%s : gris des cochés %.2f:1", c.Key, r)
		}
		rule := fmt.Sprintf(".c-%s { background-color: %s; }", c.Key, c.Hex)
		if !strings.Contains(string(css), rule) {
			t.Errorf("app.css : règle %q absente", rule)
		}
	}
	if !strings.Contains(string(css), "--ink: "+inkColor+";") || !strings.Contains(string(css), "--muted: "+checkedGray+";") {
		t.Error("app.css : --ink ou --muted différent de colors.go")
	}
}

// Les noms d'événements SSE et les raisons de retrait sont dupliqués dans app.js.
func TestEventNamesInJS(t *testing.T) {
	js, _ := staticFS.ReadFile("web/static/app.js")
	for _, n := range []string{evList, evListRemoved, evItem, evItemRemoved, evItemsRemoved, evItemsMoved, reasonUnshared} {
		if !strings.Contains(string(js), "'"+n+"'") {
			t.Errorf("app.js : %q absent", n)
		}
	}
}

// ---------------------------------------------------------------------------
// SSE : survit aux timeouts du serveur ; nombre de flux limité ; coupé si la session disparaît.

func TestSSESurvivesServerTimeoutsAndLimits(t *testing.T) {
	h := newHarness(t)
	h.app.heartbeat = 100 * time.Millisecond
	srv := httptest.NewUnstartedServer(h.app.routes())
	srv.Config.ReadTimeout = 300 * time.Millisecond
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	a := h.addUser("alice")
	s := h.session(a)

	ev := openSSE(t, srv.URL, s)
	time.Sleep(time.Second) // bien au-delà des timeouts
	h.createList(s, "après une seconde")
	if e := ev.next(t, time.Second); e.Name != "list" {
		t.Fatalf("événement : %v", e)
	}

	for i := 1; i < maxStreamsPerSession; i++ {
		openSSE(t, srv.URL, s)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.Token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("flux de trop : %d", resp.StatusCode)
	}

	// Session révoquée (ligne de commande, autre processus) : le flux se coupe au battement suivant.
	h.db.Exec(`DELETE FROM sessions`)
	select {
	case <-ev.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("flux toujours ouvert après révocation")
	}
}

func TestDatabasePragmas(t *testing.T) {
	h := newHarness(t)
	var mode string
	var fk int
	h.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	h.db.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	if mode != "wal" || fk != 1 {
		t.Fatalf("journal_mode=%s foreign_keys=%d", mode, fk)
	}
	// Réouverture : les migrations ne se rejouent pas.
	h2 := newHarnessAt(t, h.dir)
	var v int
	h2.db.QueryRow("PRAGMA user_version").Scan(&v)
	if v != 2 {
		t.Fatalf("user_version=%d", v)
	}
	var maxPages int
	h2.db.QueryRow("PRAGMA max_page_count").Scan(&maxPages)
	if maxPages != dbMaxPages {
		t.Fatalf("max_page_count=%d", maxPages)
	}
}

// Une base créée avant la suppression du TOTP (migration 001 seule, avec un compte)
// passe en version 2 sans perdre le compte ni ses sessions.
func TestMigrationDropTOTP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "listes.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	m1, _ := migrationsFS.ReadFile("migrations/001_init.sql")
	if _, err := old.Exec(string(m1) + `; PRAGMA user_version = 1;
		INSERT INTO users (id, name, password_hash, totp_secret, totp_last_step, created_at) VALUES ('u1', 'alice', 'h', 'SECRET', 5, 0);
		INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen_at) VALUES (x'00', 'u1', 'c', 0, 0);`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	h := newHarnessAt(t, dir)
	u, err := userByName(h.db, "alice")
	if err != nil || u.PasswordHash != "h" {
		t.Fatalf("compte perdu : %v", err)
	}
	var n int
	h.db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n)
	h.db.QueryRow(`SELECT count(*) + ? FROM pragma_table_info('users') WHERE name LIKE 'totp%'`, n).Scan(&n)
	if n != 1 {
		t.Fatal("migration 002 incorrecte")
	}
}

// Limite globale de la base : au-delà, les écritures sont refusées proprement (507),
// la lecture et la suppression continuent de marcher.
func TestDatabaseSizeLimit(t *testing.T) {
	saved := dbMaxPages
	dbMaxPages = 64 // 256 Kio
	t.Cleanup(func() { dbMaxPages = saved })
	h := newHarness(t)
	s := h.session(h.addUser("alice"))
	full := false
	var last *httptest.ResponseRecorder
	for i := 0; i < 50 && !full; i++ {
		l := h.createList(s, fmt.Sprintf("L%d", i))
		for j := 0; j < 100; j++ {
			last = h.do(s, "POST", "/api/lists/"+l.ID+"/items", map[string]any{"text": strings.Repeat("x", maxItemText)})
			if last.Code != http.StatusOK {
				full = true
				break
			}
		}
	}
	if !full || last.Code != http.StatusInsufficientStorage || !strings.Contains(last.Body.String(), "Stockage plein") {
		t.Fatalf("limite non atteinte ou mauvaise réponse : %d %s", last.Code, last.Body.String())
	}
	// Lecture : toujours possible.
	st := h.state(s)
	if len(st.Lists) == 0 {
		t.Fatal("lecture impossible")
	}
	// Libérer de la place : supprimer une liste remet l'appli en état d'écrire.
	h.expect(http.StatusNoContent, s, "DELETE", "/api/lists/"+st.Lists[0].ID, nil)
	h.expect(http.StatusOK, s, "POST", "/api/lists", map[string]any{"name": "après ménage"})
}

// Journal borné : un même message n'est écrit qu'une fois par minute.
func TestThrottledLog(t *testing.T) {
	msg := "test " + newID()
	if ok, _ := throttled(msg); !ok {
		t.Fatal("premier message non écrit")
	}
	for i := 0; i < 100; i++ {
		if ok, _ := throttled(msg); ok {
			t.Fatal("message répété écrit")
		}
	}
	logThrottle.Lock()
	logThrottle.last[msg] = time.Now().Add(-2 * logEvery)
	logThrottle.Unlock()
	if ok, skipped := throttled(msg); !ok || skipped != 100 {
		t.Fatalf("après une minute : ok=%v sautés=%d", ok, skipped)
	}
}
