package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed web/templates/*.html
var templatesFS embed.FS

//go:embed web/static
var staticFS embed.FS

type App struct {
	cfg  config
	db   *sql.DB
	hub  *hub
	tmpl *template.Template
	// Fichiers statiques précalculés (contenu, type, ETag).
	static map[string]staticFile
	// Durée minimale d'une tentative de connexion : même temps de réponse quelle que soit la cause d'échec.
	loginMin time.Duration
	// Battement du flux SSE ; la session est revérifiée à chaque battement.
	heartbeat time.Duration
	// Sérialise écritures et publications SSE (voir commitAndPublish).
	writeMu sync.Mutex
}

type staticFile struct {
	body  []byte
	ctype string
	etag  string
}

func newApp(cfg config, db *sql.DB) *App {
	a := &App{
		cfg:       cfg,
		db:        db,
		hub:       newHub(),
		tmpl:      template.Must(template.ParseFS(templatesFS, "web/templates/*.html")),
		static:    map[string]staticFile{},
		loginMin:  800 * time.Millisecond,
		heartbeat: 20 * time.Second,
	}
	fs.WalkDir(staticFS, "web/static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, _ := staticFS.ReadFile(p)
		sum := sha256.Sum256(body)
		ctype := mime.TypeByExtension(path.Ext(p))
		if strings.HasSuffix(p, ".webmanifest") {
			ctype = "application/manifest+json"
		}
		a.static[strings.TrimPrefix(p, "web")] = staticFile{body, ctype, `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	return a
}

// commitAndPublish exécute fn dans une transaction puis publie ses événements, le tout sous un
// même verrou : l'ordre des événements suit celui des commits.
func (a *App) commitAndPublish(fn func(tx *sql.Tx) ([]event, error)) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	evs, err := fn(tx)
	if err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.hub.publish(evs...)
	return nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /static/", a.serveStatic)
	mux.HandleFunc("GET /manifest.webmanifest", a.serveStatic)
	mux.HandleFunc("GET /sw.js", a.serveStatic) // à la racine : sa portée couvre toute l'appli
	mux.HandleFunc("GET /login", a.loginPage)
	mux.HandleFunc("POST /login", a.loginPost)
	mux.HandleFunc("POST /logout", a.authedPage(a.logout))
	mux.HandleFunc("GET /{$}", a.authedPage(a.appPage))

	mux.HandleFunc("GET /api/state", a.authedAPI(a.apiState))
	mux.HandleFunc("GET /api/events", a.authedAPI(a.apiEvents))
	mux.HandleFunc("POST /api/lists", a.authedAPI(a.apiCreateList))
	mux.HandleFunc("PATCH /api/lists/{id}", a.authedAPI(a.apiUpdateList))
	mux.HandleFunc("DELETE /api/lists/{id}", a.authedAPI(a.apiDeleteList))
	mux.HandleFunc("POST /api/lists/{id}/items", a.authedAPI(a.apiCreateItem))
	mux.HandleFunc("POST /api/lists/{id}/clear-checked", a.authedAPI(a.apiClearChecked))
	mux.HandleFunc("PATCH /api/items/{id}", a.authedAPI(a.apiUpdateItem))
	mux.HandleFunc("DELETE /api/items/{id}", a.authedAPI(a.apiDeleteItem))
	mux.HandleFunc("POST /api/items/{id}/move", a.authedAPI(a.apiMoveItem))
	return securityHeaders(limitBody(mux))
}

// ---------------------------------------------------------------------------
// Middlewares

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
	"manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

const permissionsPolicy = "accelerometer=(), autoplay=(), camera=(), display-capture=(), encrypted-media=(), " +
	"fullscreen=(), geolocation=(), gyroscope=(), hid=(), idle-detection=(), magnetometer=(), microphone=(), " +
	"midi=(), payment=(), picture-in-picture=(), publickey-credentials-get=(), screen-wake-lock=(), serial=(), " +
	"usb=(), web-share=(), xr-spatial-tracking=()"

// securityHeaders : à compléter au reverse proxy par HSTS, nosniff et Referrer-Policy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", permissionsPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cache-Control", "no-store") // remplacé par no-cache pour les fichiers statiques
		next.ServeHTTP(w, r)
	})
}

func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// clientIP : l'adresse de la connexion, sauf si elle vient d'un relais de confiance.
//
// Chaîne de confiance : navigateur → reverse proxy, qui écrit l'IP du client dans
// X-Forwarded-For → port publié 127.0.0.1:5240 → conteneur. Seule une connexion
// venant de 127.0.0.1 (ou d'une adresse de LISTES_TRUSTED_PROXIES : la passerelle
// du réseau Docker, par laquelle arrive le port publié) peut porter un
// X-Forwarded-For crédible. On en prend la dernière valeur : celle ajoutée par le
// reverse proxy. Dans tous les autres cas, l'en-tête est ignoré.
func clientIP(r *http.Request, trusted []string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !slices.Contains(trusted, host) {
		return host
	}
	xff := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if xff == "" {
		return host
	}
	parts := strings.Split(xff, ",")
	if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
		return ip.String()
	}
	return host
}

// sameOrigin : défense CSRF en plus de SameSite=Strict et du jeton synchroniseur.
// Origin obligatoire et égale à l'origine publique ; Sec-Fetch-Site, s'il est
// présent, doit valoir same-origin.
func (a *App) sameOrigin(r *http.Request) bool {
	if r.Header.Get("Origin") != a.cfg.Origin {
		return false
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" {
		return false
	}
	return true
}

func isUnsafe(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

// authedPage et authedAPI exigent une session valide ; pour toute requête qui modifie,
// ils vérifient aussi l'origine et le jeton CSRF de la session. Sans session, une
// page redirige vers /login et l'API répond 401 en JSON.
func (a *App) authedPage(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return a.authed(false, h)
}

func (a *App) authedAPI(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return a.authed(true, h)
}

func (a *App) authed(api bool, h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var token string
		if c, err := r.Cookie(sessionCookie); err == nil {
			token = c.Value
		}
		s, refreshed, err := lookupSession(a.db, token)
		if err != nil {
			if !errors.Is(err, errNotFound) {
				errorLog("session", err)
			}
			if api {
				writeJSON(w, http.StatusUnauthorized, apiError{"Session expirée, reconnecte-toi."})
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}
		if refreshed {
			setSessionCookie(w, token, s)
		}
		if isUnsafe(r.Method) {
			sent := r.Header.Get("X-CSRF-Token")
			if sent == "" && !api {
				sent = r.PostFormValue("csrf")
			}
			if !a.sameOrigin(r) || subtle.ConstantTimeCompare([]byte(sent), []byte(s.CSRF)) != 1 {
				writeJSON(w, http.StatusForbidden, apiError{"Requête refusée."})
				return
			}
		}
		h(w, r, s)
	}
}

// setCookie : tous les cookies sont des __Host- (Secure, Path=/), HttpOnly et SameSite=Strict.
func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func setSessionCookie(w http.ResponseWriter, token string, s *session) {
	maxAge := time.Duration(s.expiry()-nowMs()) * time.Millisecond
	setCookie(w, sessionCookie, token, int(maxAge.Seconds()))
}

func clearCookie(w http.ResponseWriter, name string) {
	setCookie(w, name, "", -1)
}

// ---------------------------------------------------------------------------
// Pages

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	if err := a.db.PingContext(r.Context()); err != nil {
		http.Error(w, "db", http.StatusServiceUnavailable)
		return
	}
	io.WriteString(w, "ok\n")
}

func (a *App) serveStatic(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p == "/manifest.webmanifest" || p == "/sw.js" {
		p = "/static" + p
	}
	f, ok := a.static[p]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", f.etag)
	if r.Header.Get("If-None-Match") == f.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", f.ctype)
	w.Write(f.body)
}

func (a *App) render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := a.tmpl.ExecuteTemplate(w, name, data); err != nil {
		errorLog("template "+name, err)
	}
}

// appPageData : les limites viennent de store.go, app.js les lit dans la page.
type appPageData struct {
	UserID, Name, CSRF       string
	Colors                   []color
	MaxItemText, MaxListName int
	DefaultListName          string
}

func (a *App) appPage(w http.ResponseWriter, r *http.Request, s *session) {
	a.render(w, http.StatusOK, "app.html", appPageData{UserID: s.UserID, Name: s.UserName, CSRF: s.CSRF, Colors: colors,
		MaxItemText: maxItemText, MaxListName: maxListName, DefaultListName: defaultListName})
}

// housekeeping : purge horaire des sessions expirées et des vieux compteurs d'échecs.
func (a *App) housekeeping(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		n := nowMs()
		if _, err := a.db.Exec(`DELETE FROM sessions WHERE last_seen_at < ? OR created_at < ?`,
			n-sessionIdle.Milliseconds(), n-sessionMax.Milliseconds()); err != nil {
			slog.Error("purge des sessions", "err", err)
		}
		if _, err := a.db.Exec(`DELETE FROM login_attempts WHERE last_failure_at < ? AND locked_until < ?`,
			n-attemptsWindow.Milliseconds(), n); err != nil {
			slog.Error("purge des tentatives", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ---------------------------------------------------------------------------
// JSON

type apiError struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// decodeJSON : Content-Type JSON, champs connus seulement, un seul objet, ≤ 16 Ko (limitBody).
func decodeJSON(r *http.Request, v any) error {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		return invalidError("Content-Type attendu : application/json")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return err
		}
		return invalidError("JSON invalide")
	}
	if dec.More() {
		return invalidError("JSON invalide")
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return err
	}
	return nil
}

func writeErr(w http.ResponseWriter, err error) {
	var inv invalidError
	var mbe *http.MaxBytesError
	switch {
	case errors.Is(err, errNotFound):
		writeJSON(w, http.StatusNotFound, apiError{"Introuvable."})
	case errors.Is(err, errForbidden):
		writeJSON(w, http.StatusForbidden, apiError{"Réservé au propriétaire de la liste."})
	case errors.Is(err, errLimit):
		writeJSON(w, http.StatusConflict, apiError{"Limite atteinte."})
	case errors.As(err, &inv):
		writeJSON(w, http.StatusBadRequest, apiError{inv.Error()})
	case errors.As(err, &mbe):
		writeJSON(w, http.StatusRequestEntityTooLarge, apiError{"Requête trop grosse."})
	case isStorageFull(err):
		errorLog("base pleine", err)
		writeJSON(w, http.StatusInsufficientStorage, apiError{"Stockage plein : retire des éléments ou des listes pour continuer."})
	default:
		errorLog("requête", err)
		writeJSON(w, http.StatusInternalServerError, apiError{"Erreur interne."})
	}
}
