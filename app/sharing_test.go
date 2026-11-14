package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Partage avec des comptes précis : seuls les membres voient la liste et en modifient
// les éléments ; le partage reste au propriétaire.
func TestShareWithMembers(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	bobUser := h.addUser("bob")
	bob, carol := h.session(bobUser), h.session(h.addUser("carol"))
	l := h.createList(alice, "Courses des parents")
	it := h.createItem(alice, l.ID, "lait")

	var after List
	h.mustDo(alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID, alice.User.ID, bobUser.ID}}, &after)
	if len(after.Members) != 1 || after.Members[0].ID != bobUser.ID || after.Members[0].Name != "bob" || after.Shared {
		t.Fatalf("membres : %+v", after.Members)
	}
	if !hasList(h.state(bob), l.ID) || hasList(h.state(carol), l.ID) {
		t.Fatal("visibilité")
	}
	h.expect(http.StatusOK, bob, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true})
	h.expect(http.StatusNotFound, carol, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true})
	h.expect(http.StatusForbidden, bob, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{}})
	h.expect(http.StatusForbidden, bob, "POST", "/api/lists/"+l.ID+"/public-link", nil)
	h.expect(http.StatusBadRequest, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{newID()}})
	h.expect(http.StatusBadRequest, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{"pas-un-id"}})

	// Les autres comptes, pour le panneau de partage : sans soi-même.
	var acc []member
	h.mustDo(alice, "GET", "/api/accounts", nil, &acc)
	if len(acc) != 2 || acc[0].Name != "bob" || acc[1].Name != "carol" {
		t.Fatalf("comptes : %+v", acc)
	}

	h.mustDo(alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{}}, &after)
	if hasList(h.state(bob), l.ID) {
		t.Fatal("membre retiré : liste encore visible")
	}
}

// Temps réel : qui découvre la liste la reçoit avec ses éléments, qui la perd reçoit un
// retrait, les autres rien.
func TestShareMembersEvents(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	bobUser := h.addUser("bob")
	bob, carol := h.session(bobUser), h.session(h.addUser("carol"))
	l := h.createList(alice, "Courses")
	h.createItem(alice, l.ID, "pain")
	srv := httptest.NewServer(h.app.routes())
	t.Cleanup(srv.Close) // après la fermeture des flux
	evB, evC := openSSE(t, srv.URL, bob), openSSE(t, srv.URL, carol)

	h.expect(http.StatusOK, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID}})
	ev := evB.next(t, 2*time.Second)
	var got List
	json.Unmarshal([]byte(ev.Data), &got)
	if ev.Name != evList || got.ID != l.ID || len(got.Items) != 1 {
		t.Fatalf("bob : %s %s", ev.Name, ev.Data)
	}
	evC.none(t, 200*time.Millisecond)

	h.expect(http.StatusOK, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{}})
	if ev := evB.next(t, 2*time.Second); ev.Name != evListRemoved || !strings.Contains(ev.Data, reasonUnshared) {
		t.Fatalf("bob : %s %s", ev.Name, ev.Data)
	}
	evC.none(t, 200*time.Millisecond)
}

// publicLink : le propriétaire crée le lien ; renvoie le jeton.
func (h *harness) publicLink(s *testSession, listID string) string {
	h.t.Helper()
	var res publicLinkResponse
	h.mustDo(s, "POST", "/api/lists/"+listID+"/public-link", nil, &res)
	token, ok := strings.CutPrefix(res.URL, testOrigin+"/s/")
	if !ok || !publicTokenRe.MatchString(token) {
		h.t.Fatalf("lien : %q", res.URL)
	}
	return token
}

// anon : requête d'un visiteur sans compte, depuis la page du lien.
func (h *harness) anon(method, path string, body any) *httptest.ResponseRecorder {
	req := h.req(nil, method, path, body)
	return h.serve(req)
}

func TestPublicLink(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	l := h.createList(alice, "Courses")
	milk := h.createItem(alice, l.ID, "lait")
	var cat Item
	h.mustDo(alice, "POST", "/api/lists/"+l.ID+"/items", map[string]any{"text": "Frais", "category": true}, &cat)
	other := h.createItem(alice, h.createList(alice, "Privée").ID, "secret")

	if w := h.anon("GET", "/s/"+strings.Repeat("a", 43), nil); w.Code != http.StatusNotFound {
		t.Fatalf("jeton inconnu : %d", w.Code)
	}
	token := h.publicLink(alice, l.ID)

	w := h.anon("GET", "/s/"+token, nil)
	if w.Code != http.StatusOK || w.Header().Get("X-Robots-Tag") == "" || w.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatalf("page : %d %v", w.Code, w.Header())
	}
	// L'état ne dit rien des comptes ni du partage.
	st := h.anon("GET", "/s/"+token+"/state", nil).Body.String()
	for _, leak := range []string{alice.User.ID, "members", "shared", "owner_id", "public"} {
		if strings.Contains(st, leak) {
			t.Errorf("état public : %q présent dans %s", leak, st)
		}
	}

	check := func(itemID string, body any) int {
		return h.anon("POST", "/s/"+token+"/items/"+itemID, body).Code
	}
	if c := check(milk.ID, map[string]any{"checked": true}); c != http.StatusOK {
		t.Fatalf("cocher : %d", c)
	}
	var got Item
	for _, li := range h.state(alice).Lists {
		for _, it := range li.Items {
			if it.ID == milk.ID {
				got = it
			}
		}
	}
	if !got.Checked {
		t.Fatal("coche non enregistrée")
	}
	// Seulement cocher : ni texte, ni catégorie, ni élément d'une autre liste.
	if c := check(milk.ID, map[string]any{"text": "bière"}); c != http.StatusBadRequest {
		t.Fatalf("texte : %d", c)
	}
	if c := check(cat.ID, map[string]any{"checked": true}); c != http.StatusBadRequest {
		t.Fatalf("catégorie : %d", c)
	}
	if c := check(other.ID, map[string]any{"checked": true}); c != http.StatusNotFound {
		t.Fatalf("autre liste : %d", c)
	}
	for _, r := range []struct{ method, path string }{
		{"POST", "/s/" + token + "/items/" + milk.ID + "/move"},
		{"DELETE", "/s/" + token + "/items/" + milk.ID},
		{"PATCH", "/s/" + token},
	} {
		if c := h.anon(r.method, r.path, map[string]any{}).Code; c != http.StatusNotFound && c != http.StatusMethodNotAllowed {
			t.Errorf("%s %s : %d", r.method, r.path, c)
		}
	}
	// Sans Origin du site : refusé (CSRF).
	req := httptest.NewRequest("POST", "/s/"+token+"/items/"+milk.ID, strings.NewReader(`{"checked":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	if w := h.serve(req); w.Code != http.StatusForbidden {
		t.Fatalf("autre origine : %d", w.Code)
	}

	// Nouveau lien : l'ancien ne marche plus. Révocation : plus rien.
	newToken := h.publicLink(alice, l.ID)
	if h.anon("GET", "/s/"+token+"/state", nil).Code != http.StatusNotFound || h.anon("GET", "/s/"+newToken+"/state", nil).Code != http.StatusOK {
		t.Fatal("remplacement du lien")
	}
	var link publicLinkResponse
	h.mustDo(alice, "GET", "/api/lists/"+l.ID+"/public-link", nil, &link)
	if link.URL != publicURL(testOrigin, newToken) {
		t.Fatalf("lien relu : %q", link.URL)
	}
	h.expect(http.StatusNoContent, alice, "DELETE", "/api/lists/"+l.ID+"/public-link", nil)
	if h.anon("GET", "/s/"+newToken+"/state", nil).Code != http.StatusNotFound {
		t.Fatal("lien révoqué encore valable")
	}
}

// Flux du lien public : les éléments, le nom et la couleur ; rien sur les comptes. Le lien
// remplacé coupe le flux.
func TestPublicEvents(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	l := h.createList(alice, "Courses")
	it := h.createItem(alice, l.ID, "lait")
	token := h.publicLink(alice, l.ID)
	srv := httptest.NewServer(h.app.routes())
	t.Cleanup(srv.Close) // après la fermeture des flux
	pub := openPublicSSE(t, srv.URL, token)

	h.expect(http.StatusOK, alice, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true})
	if ev := pub.next(t, 2*time.Second); ev.Name != evItem || !strings.Contains(ev.Data, `"checked":true`) {
		t.Fatalf("élément : %s %s", ev.Name, ev.Data)
	}
	h.expect(http.StatusOK, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": "Courses du samedi"})
	ev := pub.next(t, 2*time.Second)
	if ev.Name != evList || !strings.Contains(ev.Data, "Courses du samedi") || strings.Contains(ev.Data, alice.User.ID) || strings.Contains(ev.Data, "shared") {
		t.Fatalf("liste : %s %s", ev.Name, ev.Data)
	}
	// Une autre liste : rien.
	h.createItem(alice, h.createList(alice, "Autre").ID, "x")
	pub.none(t, 200*time.Millisecond)

	h.publicLink(alice, l.ID)
	select {
	case <-pub.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("flux de l'ancien lien non coupé")
	}
}

func TestPublicLimits(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	l := h.createList(alice, "Courses")
	it := h.createItem(alice, l.ID, "lait")
	token := h.publicLink(alice, l.ID)
	freezeTime(t)
	publicWrites = rateLimiter{counts: map[string]int{}}
	t.Cleanup(func() { publicWrites = rateLimiter{counts: map[string]int{}} })
	for i := 0; i < publicWritesPerMinute; i++ {
		if c := h.anon("POST", "/s/"+token+"/items/"+it.ID, map[string]any{"checked": i%2 == 0}).Code; c != http.StatusOK {
			t.Fatalf("écriture %d : %d", i, c)
		}
	}
	if c := h.anon("POST", "/s/"+token+"/items/"+it.ID, map[string]any{"checked": true}).Code; c != http.StatusTooManyRequests {
		t.Fatalf("au-delà de la limite : %d", c)
	}

	// Flux anonymes : plafond par IP, sans toucher aux flux des comptes.
	for i := 0; i < maxPublicStreamsPerIP; i++ {
		if h.app.hub.subscribePublic(l.ID, "198.51.100.1") == nil {
			t.Fatal("flux refusé sous le plafond")
		}
	}
	if h.app.hub.subscribePublic(l.ID, "198.51.100.1") != nil {
		t.Fatal("plafond par IP dépassé")
	}
	if h.app.hub.subscribe(alice.User.ID, []byte("session")) == nil {
		t.Fatal("les flux anonymes ont privé un compte du sien")
	}
}

// Liste supprimée : les visiteurs du lien le voient tout de suite.
func TestPublicListDeleted(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	l := h.createList(alice, "Courses")
	token := h.publicLink(alice, l.ID)
	srv := httptest.NewServer(h.app.routes())
	t.Cleanup(srv.Close) // après la fermeture des flux
	pub := openPublicSSE(t, srv.URL, token)
	h.expect(http.StatusNoContent, alice, "DELETE", "/api/lists/"+l.ID, nil)
	if ev := pub.next(t, 2*time.Second); ev.Name != evListRemoved {
		t.Fatalf("%s %s", ev.Name, ev.Data)
	}
	if h.anon("GET", "/s/"+token, nil).Code != http.StatusNotFound {
		t.Fatal("page d'une liste supprimée")
	}
}
