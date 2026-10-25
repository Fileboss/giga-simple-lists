package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testOrigin = "https://listes.test"
const testPassword = "correct-horse-battery-staple"

type harness struct {
	t   *testing.T
	app *App
	db  *sql.DB
	dir string
}

type testUser struct {
	ID, Name string
}

type testSession struct {
	Token, CSRF string
	User        testUser
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	return newHarnessAt(t, dir)
}

func newHarnessAt(t *testing.T, dir string) *harness {
	t.Helper()
	db, err := openDB(filepath.Join(dir, "listes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	app := newApp(config{Addr: ":0", Origin: testOrigin, TrustedProxies: []string{"127.0.0.1"}}, db)
	app.loginMin = 0
	return &harness{t: t, app: app, db: db, dir: dir}
}

// addUser crée un compte avec un mot de passe connu.
func (h *harness) addUser(name string) testUser {
	h.t.Helper()
	var out bytes.Buffer
	if err := adminCommand(&out, strings.NewReader(testPassword+"\n"), h.db, []string{"user", "add", name, "--password-stdin"}); err != nil {
		h.t.Fatal(err)
	}
	u, err := userByName(h.db, name)
	if err != nil {
		h.t.Fatal(err)
	}
	return testUser{ID: u.ID, Name: u.Name}
}

// session ouvre une session directement en base (la connexion elle-même est testée à part).
func (h *harness) session(u testUser) *testSession {
	h.t.Helper()
	token, s, err := createSession(h.db, u.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	return &testSession{Token: token, CSRF: s.CSRF, User: u}
}

// req construit une requête « navigateur légitime » : cookie, Origin, Sec-Fetch-Site, jeton CSRF.
func (h *harness) req(s *testSession, method, path string, body any) *http.Request {
	var r io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		r = strings.NewReader(b)
	default:
		js, _ := json.Marshal(b)
		r = bytes.NewReader(js)
	}
	req := httptest.NewRequest(method, path, r)
	req.RemoteAddr = "203.0.113.7:4444"
	if s != nil {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.Token})
		req.Header.Set("X-CSRF-Token", s.CSRF)
	}
	if isUnsafe(method) {
		req.Header.Set("Origin", testOrigin)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func (h *harness) serve(req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.app.routes().ServeHTTP(w, req)
	return w
}

func (h *harness) do(s *testSession, method, path string, body any) *httptest.ResponseRecorder {
	return h.serve(h.req(s, method, path, body))
}

// mustDo : la requête doit réussir ; décode la réponse JSON dans out (si non nil).
func (h *harness) mustDo(s *testSession, method, path string, body any, out any) {
	h.t.Helper()
	w := h.do(s, method, path, body)
	if w.Code >= 300 {
		h.t.Fatalf("%s %s : %d %s", method, path, w.Code, w.Body.String())
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) expect(code int, s *testSession, method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	w := h.do(s, method, path, body)
	if w.Code != code {
		h.t.Fatalf("%s %s : attendu %d, obtenu %d %s", method, path, code, w.Code, w.Body.String())
	}
	return w
}

func (h *harness) createList(s *testSession, name string) *List {
	h.t.Helper()
	var l List
	h.mustDo(s, "POST", "/api/lists", map[string]string{"name": name}, &l)
	return &l
}

func (h *harness) createItem(s *testSession, listID, text string) *Item {
	h.t.Helper()
	var it Item
	h.mustDo(s, "POST", "/api/lists/"+listID+"/items", map[string]string{"text": text}, &it)
	return &it
}

func (h *harness) state(s *testSession) stateResponse {
	h.t.Helper()
	var st stateResponse
	h.mustDo(s, "GET", "/api/state", nil, &st)
	return st
}

func hasList(st stateResponse, id string) bool {
	for _, l := range st.Lists {
		if l.ID == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Connexion par le formulaire

type loginResult struct {
	Code   int
	Body   string
	Cookie *http.Cookie
}

func (h *harness) loginForm(ip, name, password string) loginResult {
	h.t.Helper()
	get := httptest.NewRequest("GET", "/login", nil)
	w := h.serve(get)
	var csrfCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == loginCSRFCookie {
			csrfCookie = c
		}
	}
	if csrfCookie == nil {
		h.t.Fatal("pas de cookie CSRF de connexion")
	}
	form := url.Values{"csrf": {csrfCookie.Value}, "name": {name}, "password": {password}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", testOrigin)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.AddCookie(csrfCookie)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", ip)
	w = h.serve(req)
	res := loginResult{Code: w.Code, Body: w.Body.String()}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			res.Cookie = c
		}
	}
	return res
}

// freezeTime fige l'horloge de l'appli ; renvoie une fonction pour l'avancer.
func freezeTime(t *testing.T) func(time.Duration) {
	t.Helper()
	cur := time.Now()
	now = func() time.Time { return cur }
	t.Cleanup(func() { now = time.Now })
	return func(d time.Duration) { cur = cur.Add(d) }
}

// ---------------------------------------------------------------------------
// Flux SSE réel (serveur HTTP de test)

type sseEvent struct {
	Name string
	Data string
}

type sseClient struct {
	events chan sseEvent
	closed chan struct{}
	resp   *http.Response
}

func openSSE(t *testing.T, srvURL string, s *testSession) *sseClient {
	t.Helper()
	req, _ := http.NewRequest("GET", srvURL+"/api/events", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.Token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("SSE : %d", resp.StatusCode)
	}
	c := &sseClient{events: make(chan sseEvent, 100), closed: make(chan struct{}), resp: resp}
	t.Cleanup(func() { resp.Body.Close() })
	go func() {
		defer close(c.closed)
		sc := bufio.NewScanner(resp.Body)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.Name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.Data = strings.TrimPrefix(line, "data: ")
			case line == "" && ev.Name == "ping":
				ev = sseEvent{}
			case line == "" && ev.Name != "":
				c.events <- ev
				ev = sseEvent{}
			}
		}
	}()
	return c
}

// next attend un événement (ou échoue au bout de d).
func (c *sseClient) next(t *testing.T, d time.Duration) sseEvent {
	t.Helper()
	select {
	case ev := <-c.events:
		return ev
	case <-time.After(d):
		t.Fatal("aucun événement SSE reçu")
	}
	return sseEvent{}
}

// none vérifie qu'aucun événement n'arrive pendant d.
func (c *sseClient) none(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case ev := <-c.events:
		t.Fatalf("événement inattendu : %s %s", ev.Name, ev.Data)
	case <-time.After(d):
	}
}
