package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// useLang passe l'instance dans une langue le temps d'un test.
func useLang(t *testing.T, l string) {
	t.Helper()
	old := lang
	if err := setLang(l); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lang = old })
}

// Chaque langue a les mêmes clés, et chaque clé utilisée (Go, gabarits, app.js) existe.
func TestMessagesComplete(t *testing.T) {
	en := messages["en"]
	for l, m := range messages {
		for k := range en {
			if m[k] == "" {
				t.Errorf("%s : clé %q absente", l, k)
			}
		}
		for k := range m {
			if _, ok := en[k]; !ok {
				t.Errorf("%s : clé %q inconnue en anglais", l, k)
			}
		}
	}

	used := map[string]string{}
	goCall := regexp.MustCompile(`\b(?:T|tf|invalidError)\("([a-zA-Z.]+)"`)
	files, _ := os.ReadDir(".")
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		src, _ := os.ReadFile(f.Name())
		for _, m := range goCall.FindAllStringSubmatch(string(src), -1) {
			used[m[1]] = f.Name()
		}
	}
	tmplCall := regexp.MustCompile(`\{\{t "([a-zA-Z.]+)"\}\}`)
	fs.WalkDir(templatesFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		src, _ := templatesFS.ReadFile(p)
		for _, m := range tmplCall.FindAllStringSubmatch(string(src), -1) {
			used[m[1]] = p
		}
		return nil
	})
	js, _ := staticFS.ReadFile("web/static/app.js")
	for _, m := range regexp.MustCompile(`\bt\('([a-zA-Z]+)'`).FindAllStringSubmatch(string(js), -1) {
		used["js."+m[1]] = "app.js"
	}
	// Choix entre deux clés : t(cond ? 'a' : 'b').
	for _, m := range regexp.MustCompile(`\bt\([^()]*\? '([a-zA-Z]+)' : '([a-zA-Z]+)'`).FindAllStringSubmatch(string(js), -1) {
		used["js."+m[1]], used["js."+m[2]] = "app.js", "app.js"
	}
	for _, c := range colors {
		used["color."+c.Key] = "colors.go"
	}
	if len(used) < 100 {
		t.Fatalf("seulement %d clés trouvées : expressions régulières à revoir", len(used))
	}
	for k, where := range used {
		if _, ok := en[k]; !ok {
			t.Errorf("%s : clé %q absente de i18n.go", where, k)
		}
	}
}

func TestSetLang(t *testing.T) {
	useLang(t, "en")
	for _, ok := range []string{"", "en", "fr", " FR "} {
		if err := setLang(ok); err != nil {
			t.Errorf("%q : %v", ok, err)
		}
	}
	for _, bad := range []string{"de", "english", "fr_FR"} {
		if setLang(bad) == nil {
			t.Errorf("%q accepté", bad)
		}
	}
	if setLang("") != nil || lang != "en" {
		t.Fatal("anglais par défaut attendu")
	}
}

// La langue de l'instance s'applique aux pages, aux messages d'app.js, au manifeste et
// aux erreurs de l'API.
func TestLanguageEverywhere(t *testing.T) {
	for _, c := range []struct{ lang, title, add, msg, manifest, notFound string }{
		{"en", "<title>Lists</title>", `placeholder="Add…"`, `"uncheckAll":"Uncheck all"`, `"name": "Lists"`, "Not found."},
		{"fr", "<title>Listes</title>", `placeholder="Ajouter…"`, `"uncheckAll":"Tout décocher"`, `"name": "Listes"`, "Introuvable."},
	} {
		t.Run(c.lang, func(t *testing.T) {
			useLang(t, c.lang)
			h := newHarness(t)
			s := h.session(h.addUser("alice"))
			page := h.expect(http.StatusOK, s, "GET", "/", nil).Body.String()
			for _, want := range []string{`<html lang="` + c.lang + `">`, c.title, c.add} {
				if !strings.Contains(page, want) {
					t.Errorf("page : %q absent", want)
				}
			}
			// Bloc de données JSON lisible par app.js (le gabarit l'échappe pour le contexte script).
			start := strings.Index(page, `<script id="messages" type="application/json">`)
			end := strings.Index(page[start:], "</script>")
			raw := page[start+len(`<script id="messages" type="application/json">`) : start+end]
			var msgs map[string]string
			if err := json.Unmarshal([]byte(raw), &msgs); err != nil {
				t.Fatalf("messages illisibles : %v (%s)", err, raw)
			}
			if b, _ := json.Marshal(map[string]string{"uncheckAll": msgs["uncheckAll"]}); !strings.Contains(string(b), c.msg) {
				t.Errorf("messages : %s", b)
			}
			w := h.serve(httptest.NewRequest("GET", "/manifest.webmanifest", nil))
			if !strings.Contains(w.Body.String(), c.manifest) || !json.Valid(w.Body.Bytes()) {
				t.Errorf("manifeste : %s", w.Body.String())
			}
			w = h.do(s, "PATCH", "/api/lists/"+newID(), map[string]any{"name": "x"})
			if !strings.Contains(w.Body.String(), c.notFound) {
				t.Errorf("erreur : %s", w.Body.String())
			}
		})
	}
}
