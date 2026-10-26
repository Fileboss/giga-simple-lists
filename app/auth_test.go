package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPasswordHash(t *testing.T) {
	h := hashPassword("un mot de passe")
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("paramètres inattendus : %s", h)
	}
	if !verifyPassword(h, "un mot de passe") {
		t.Fatal("mot de passe correct refusé")
	}
	for _, bad := range []string{"", "un mot de pass", "un mot de passe "} {
		if verifyPassword(h, bad) {
			t.Fatalf("mot de passe %q accepté", bad)
		}
	}
	if hashPassword("x") == hashPassword("x") {
		t.Fatal("sel absent")
	}
	if verifyPassword("$argon2i$v=19$m=19456,t=2,p=1$AAAA$AAAA", "x") || verifyPassword("n'importe quoi", "x") {
		t.Fatal("empreinte invalide acceptée")
	}
}

func TestLoginSuccessAndCookie(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	res := h.loginForm("198.51.100.1", "alice", testPassword)
	if res.Code != http.StatusSeeOther || res.Cookie == nil {
		t.Fatalf("connexion refusée : %d %s", res.Code, res.Body)
	}
	c := res.Cookie
	// Lax : une appli installée sur l'écran d'accueil s'ouvre comme une navigation venue d'ailleurs.
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.Domain != "" {
		t.Fatalf("attributs du cookie : %+v", c)
	}
	if c.MaxAge < int((89 * 24 * time.Hour).Seconds()) {
		t.Fatalf("Max-Age trop court : %d", c.MaxAge)
	}
	if len(c.Value) < 43 { // 256 bits en base64url
		t.Fatalf("jeton trop court : %d", len(c.Value))
	}
	// Seul le hash du jeton est en base.
	var n int
	h.db.QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ?`, tokenHash(c.Value)).Scan(&n)
	if n != 1 {
		t.Fatal("session absente")
	}
	h.db.QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ? OR csrf_token = ?`, []byte(c.Value), c.Value).Scan(&n)
	if n != 0 {
		t.Fatal("jeton stocké en clair")
	}
	// La session ouvre l'appli.
	s := &testSession{Token: c.Value}
	if w := h.do(s, "GET", "/", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `name="csrf-token"`) {
		t.Fatalf("page : %d", w.Code)
	}
	// Plus de TOTP : ni champ de code dans le formulaire, ni colonne en base.
	if strings.Contains(h.serve(httptest.NewRequest("GET", "/login", nil)).Body.String(), `name="code"`) {
		t.Fatal("champ de code encore présent")
	}
	var cols int
	h.db.QueryRow(`SELECT count(*) FROM pragma_table_info('users') WHERE name LIKE 'totp%'`).Scan(&cols)
	if cols != 0 {
		t.Fatal("colonnes TOTP encore présentes")
	}
}

func TestLoginNewSessionEachTime(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	r1 := h.loginForm("198.51.100.1", "alice", testPassword)
	r2 := h.loginForm("198.51.100.1", "alice", testPassword)
	if r1.Cookie == nil || r2.Cookie == nil || r1.Cookie.Value == r2.Cookie.Value {
		t.Fatal("chaque connexion doit créer une nouvelle session")
	}
}

func TestLoginFailuresLookIdentical(t *testing.T) {
	h := newHarness(t)
	h.addUser("alice")
	for i := int64(0); i < ipRule.threshold; i++ {
		recordFailure(h.db, ipKey("198.51.100.9"), ipRule)
	}
	cases := map[string]loginResult{
		"compte inconnu":       h.loginForm("198.51.100.1", "mallory", testPassword),
		"mauvais mot de passe": h.loginForm("198.51.100.2", "alice", "mauvais"),
		"mot de passe vide":    h.loginForm("198.51.100.3", "alice", ""),
		"IP bloquée":           h.loginForm("198.51.100.9", "alice", testPassword),
	}
	var ref string
	for name, r := range cases {
		if r.Code != http.StatusUnauthorized || r.Cookie != nil {
			t.Fatalf("%s : %d", name, r.Code)
		}
		// Le corps ne diffère que par le jeton CSRF : on le neutralise.
		body := stripCSRF(r.Body)
		if !strings.Contains(body, loginFailed) {
			t.Fatalf("%s : message absent", name)
		}
		if ref == "" {
			ref = body
		} else if body != ref {
			t.Fatalf("%s : réponse différente", name)
		}
	}
}

func stripCSRF(body string) string {
	i := strings.Index(body, `name="csrf" value="`)
	if i < 0 {
		return body
	}
	j := strings.Index(body[i+19:], `"`)
	return body[:i] + body[i+19+j:]
}

func TestLoginConstantTime(t *testing.T) {
	h := newHarness(t)
	h.app.loginMin = 400 * time.Millisecond
	a := h.addUser("alice")
	for i := int64(0); i < ipRule.threshold; i++ {
		recordFailure(h.db, ipKey("198.51.100.9"), ipRule)
	}
	measure := func(ip, name, pw string) time.Duration {
		start := time.Now()
		if r := h.loginForm(ip, name, pw); r.Code != http.StatusUnauthorized {
			t.Fatalf("attendu 401, obtenu %d", r.Code)
		}
		return time.Since(start)
	}
	durations := map[string]time.Duration{
		"inconnu":   measure("198.51.100.1", "inconnu", testPassword),
		"connu":     measure("198.51.100.2", a.Name, "mauvais"),
		"bloqué":    measure("198.51.100.9", a.Name, testPassword),
		"trop long": measure("198.51.100.3", a.Name, strings.Repeat("x", 2000)),
	}
	for k, d := range durations {
		if d < 400*time.Millisecond || d > 650*time.Millisecond {
			t.Fatalf("%s : %v (attendu ≈ 400 ms)", k, d)
		}
	}
}

func TestLoginRateLimitPerIP(t *testing.T) {
	h := newHarness(t)
	freezeTime(t)
	h.addUser("alice")
	ip := "198.51.100.20"
	// 10 échecs (comptes inexistants différents : seule la limite par IP joue).
	for i := 0; i < 10; i++ {
		if r := h.loginForm(ip, "inconnu"+string(rune('a'+i)), "x"); r.Code != http.StatusUnauthorized {
			t.Fatal(r.Code)
		}
	}
	// 11ᵉ tentative depuis cette IP : bloquée, même avec les bons identifiants.
	if r := h.loginForm(ip, "alice", testPassword); r.Cookie != nil {
		t.Fatal("la 11e tentative depuis la même IP doit être bloquée")
	}
	// Survit à un redémarrage : nouvelle instance, même base.
	h2 := newHarnessAt(t, h.dir)
	if r := h2.loginForm(ip, "alice", testPassword); r.Cookie != nil {
		t.Fatal("blocage perdu au redémarrage")
	}
	// Une autre IP n'est pas concernée.
	if r := h.loginForm("198.51.100.21", "alice", testPassword); r.Cookie == nil {
		t.Fatal("autre IP bloquée à tort")
	}
}

// Par compte : pas de blocage, un ralentissement plafonné à 1 minute. Le compte reste
// utilisable depuis une autre IP, avec le bon mot de passe.
func TestLoginPerAccountSlowdown(t *testing.T) {
	// Durées de la règle : 1 s, 2 s, 4 s… plafonné à 1 min.
	want := map[int64]time.Duration{4: 0, 5: time.Second, 6: 2 * time.Second, 8: 8 * time.Second, 11: time.Minute, 40: time.Minute}
	for f, d := range want {
		if got := accountRule.delay(f); got != d {
			t.Errorf("%d échecs : %v, attendu %v", f, got, d)
		}
	}
	if accountRule.block || !ipRule.block {
		t.Fatal("seule la limite par IP bloque")
	}

	h := newHarness(t)
	saved := accountRule
	accountRule.base, accountRule.max = 100*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { accountRule = saved })
	h.addUser("alice")
	// 6 échecs depuis 6 IP différentes : aucune n'atteint la limite par IP.
	for i := 0; i < 6; i++ {
		h.loginForm(fmt.Sprintf("198.51.100.%d", 30+i), "alice", "mauvais")
	}
	var until int64
	h.db.QueryRow(`SELECT locked_until FROM login_attempts WHERE key = ?`, userKey("alice")).Scan(&until)
	if until != 0 {
		t.Fatal("le compte ne doit pas être bloqué")
	}
	// Depuis une nouvelle IP, le bon mot de passe passe, après un délai (6 échecs : 2 × base).
	start := time.Now()
	r := h.loginForm("192.0.2.50", "alice", testPassword)
	if r.Cookie == nil {
		t.Fatal("compte inutilisable après des échecs : il doit être ralenti, pas bloqué")
	}
	if d := time.Since(start); d < 200*time.Millisecond {
		t.Fatalf("pas de ralentissement : %v", d)
	}
	// Le succès remet le compteur à zéro : plus de délai.
	if d, _ := accountDelay(h.db, userKey("alice")); d != 0 {
		t.Fatalf("délai après succès : %v", d)
	}
}

// Le délai par compte allonge le délai d'écriture : la réponse arrive même au-delà de
// WriteTimeout (15 s en production, 300 ms ici).
func TestSlowedLoginSurvivesWriteTimeout(t *testing.T) {
	h := newHarness(t)
	saved := accountRule
	accountRule.base, accountRule.max = 500*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { accountRule = saved })
	h.addUser("alice")
	for i := 0; i < 5; i++ {
		recordFailure(h.db, userKey("alice"), accountRule)
	}
	srv := httptest.NewUnstartedServer(h.app.routes())
	srv.Config.WriteTimeout = 300 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var c *http.Cookie
	for _, x := range resp.Cookies() {
		if x.Name == loginCSRFCookie {
			c = x
		}
	}
	form := url.Values{"csrf": {c.Value}, "name": {"alice"}, "password": {testPassword}}
	req, _ := http.NewRequest("POST", srv.URL+"/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.AddCookie(c)
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("réponse perdue : %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("statut %d", resp.StatusCode)
	}
}

func TestSessionSlidingAndAbsoluteExpiry(t *testing.T) {
	h := newHarness(t)
	advance := freezeTime(t)
	a := h.addUser("alice")

	// Sans activité pendant plus de 90 jours : expirée.
	idle := h.session(a)
	advance(91 * 24 * time.Hour)
	h.expect(http.StatusUnauthorized, idle, "GET", "/api/state", nil)
	var n int
	h.db.QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ?`, tokenHash(idle.Token)).Scan(&n)
	if n != 0 {
		t.Fatal("session expirée non supprimée")
	}

	// Utilisée régulièrement : prolongée, mais jamais au-delà de 180 jours.
	s := h.session(a)
	for i := 0; i < 2; i++ {
		advance(89 * 24 * time.Hour)
		w := h.expect(http.StatusOK, s, "GET", "/api/state", nil)
		if !strings.Contains(w.Header().Get("Set-Cookie"), sessionCookie) {
			t.Fatal("cookie non renouvelé")
		}
	}
	advance(3 * 24 * time.Hour) // 181 jours après la création
	h.expect(http.StatusUnauthorized, s, "GET", "/api/state", nil)
}

func TestLogoutIsServerSide(t *testing.T) {
	h := newHarness(t)
	a := h.addUser("alice")
	s := h.session(a)
	req := h.req(s, "POST", "/logout", "csrf="+s.CSRF)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Del("X-CSRF-Token")
	if w := h.serve(req); w.Code != http.StatusSeeOther {
		t.Fatalf("déconnexion : %d %s", w.Code, w.Body.String())
	}
	// Le jeton volé avant la déconnexion ne sert plus à rien.
	h.expect(http.StatusUnauthorized, s, "GET", "/api/state", nil)
}

func TestRevokeAllSessions(t *testing.T) {
	h := newHarness(t)
	a, b := h.addUser("alice"), h.addUser("bob")
	sa, sb := h.session(a), h.session(b)
	var out bytes.Buffer
	if err := adminCommand(&out, nil, h.db, []string{"sessions", "revoke-all"}); err != nil {
		t.Fatal(err)
	}
	h.expect(http.StatusUnauthorized, sa, "GET", "/api/state", nil)
	h.expect(http.StatusUnauthorized, sb, "GET", "/api/state", nil)
}

func TestUserResetRevokesSessionsAndChangesPassword(t *testing.T) {
	h := newHarness(t)
	a := h.addUser("alice")
	s := h.session(a)
	var out bytes.Buffer
	if err := adminCommand(&out, nil, h.db, []string{"user", "reset", "alice"}); err != nil {
		t.Fatal(err)
	}
	h.expect(http.StatusUnauthorized, s, "GET", "/api/state", nil)
	u, _ := userByName(h.db, "alice")
	if verifyPassword(u.PasswordHash, testPassword) {
		t.Fatal("mot de passe inchangé")
	}
	if !strings.Contains(out.String(), "sessions révoquées") {
		t.Fatalf("sortie : %s", out.String())
	}
}

func TestUserAddCLI(t *testing.T) {
	h := newHarness(t)
	var out bytes.Buffer
	if err := adminCommand(&out, nil, h.db, []string{"user", "add", "zoé"}); err != nil {
		t.Fatal(err)
	}
	o := out.String()
	if !strings.Contains(o, "Mot de passe") || strings.Contains(strings.ToLower(o), "totp") {
		t.Fatalf("sortie : %s", o)
	}
	if err := adminCommand(&out, nil, h.db, []string{"user", "add", "ZOÉ"}); err == nil {
		t.Fatal("doublon accepté (insensible à la casse)")
	}
	for _, bad := range []string{"", "a b", "<script>", strings.Repeat("a", 33)} {
		if err := adminCommand(&out, nil, h.db, []string{"user", "add", bad}); err == nil {
			t.Fatalf("nom %q accepté", bad)
		}
	}
	if err := adminCommand(&out, strings.NewReader("court\n"), h.db, []string{"user", "add", "bob", "--password-stdin"}); err == nil {
		t.Fatal("mot de passe court accepté")
	}
}

func TestGeneratedPassword(t *testing.T) {
	p := generatePassword()
	if len(p) != 29 || strings.Count(p, "-") != 5 || p == generatePassword() {
		t.Fatalf("mot de passe généré : %q", p)
	}
}
