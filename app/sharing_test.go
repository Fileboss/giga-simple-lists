package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	// Compte inconnu (supprimé entre-temps) : ignoré ; identifiant mal formé : refusé.
	h.mustDo(alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID, newID()}}, &after)
	if len(after.Members) != 1 || after.Members[0].ID != bobUser.ID {
		t.Fatalf("compte inconnu : %+v", after.Members)
	}
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
	// Ni la page ni l'état ne disent quoi que ce soit des comptes (le nom du propriétaire
	// est son identifiant de connexion) ni du partage.
	if strings.Contains(w.Body.String(), "alice") {
		t.Error("page publique : nom du propriétaire présent")
	}
	st := h.anon("GET", "/s/"+token+"/state", nil).Body.String()
	for _, leak := range []string{alice.User.ID, "alice", "owner", "members", "shared", "public"} {
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
	for i := 0; i < publicWritesPerMinute; i++ {
		if c := h.anon("POST", "/s/"+token+"/items/"+it.ID, map[string]any{"checked": i%2 == 0}).Code; c != http.StatusOK {
			t.Fatalf("écriture %d : %d", i, c)
		}
	}
	if c := h.anon("POST", "/s/"+token+"/items/"+it.ID, map[string]any{"checked": true}).Code; c != http.StatusTooManyRequests {
		t.Fatalf("au-delà de la limite : %d", c)
	}

	// Flux anonymes : plafond par IP, puis par lien, sans toucher aux flux des comptes.
	for i := 0; i < maxPublicStreamsPerIP; i++ {
		if h.app.hub.subscribePublic(l.ID, "198.51.100.1") == nil {
			t.Fatal("flux refusé sous le plafond")
		}
	}
	if h.app.hub.subscribePublic(l.ID, "198.51.100.1") != nil {
		t.Fatal("plafond par IP dépassé")
	}
	for i := maxPublicStreamsPerIP; i < maxPublicStreamsPerList; i++ {
		if h.app.hub.subscribePublic(l.ID, fmt.Sprintf("2001:db8:%x::1/64", i)) == nil {
			t.Fatal("flux refusé sous le plafond par lien")
		}
	}
	if h.app.hub.subscribePublic(l.ID, "2001:db8:ffff::/64") != nil {
		t.Fatal("plafond par lien dépassé")
	}
	if h.app.hub.subscribePublic("autre-liste", "2001:db8:ffff::/64") == nil {
		t.Fatal("un lien saturé a privé les autres liens")
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

// En IPv6, tout un /64 compte comme une seule IP.
func TestIPBucket(t *testing.T) {
	for in, want := range map[string]string{
		"198.51.100.7":            "198.51.100.7",
		"2001:db8:1:2:aaaa::1":    "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb::9999": "2001:db8:1:2::/64",
		"pas-une-ip":              "pas-une-ip",
	} {
		if got := ipBucket(in); got != want {
			t.Errorf("ipBucket(%q) = %q, attendu %q", in, got, want)
		}
	}
}

// Limiteur plein : les nouvelles clés sont refusées, les compteurs existants restent.
func TestRateLimiterFull(t *testing.T) {
	freezeTime(t)
	rl := rateLimiter{counts: map[string]int{}}
	for i := 0; i < maxRateKeys; i++ {
		rl.allow(fmt.Sprint(i), 2)
	}
	if rl.allow("nouvelle", 2) {
		t.Fatal("nouvelle clé acceptée au-delà de maxRateKeys")
	}
	rl.allow("0", 2)
	if rl.allow("0", 2) {
		t.Fatal("limite remise à zéro")
	}
}

// « shared » seul (client de la version précédente, file hors ligne) ou « tout le monde » :
// plus de membres, la liste est bien privée ou bien partagée avec tous.
func TestSharedClearsMembers(t *testing.T) {
	h := newHarness(t)
	alice := h.session(h.addUser("alice"))
	bobUser := h.addUser("bob")
	bob := h.session(bobUser)
	l := h.createList(alice, "Courses")
	h.expect(http.StatusOK, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID}})
	var after List
	h.mustDo(alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": false}, &after)
	if len(after.Members) != 0 || hasList(h.state(bob), l.ID) {
		t.Fatalf("shared:false seul : membres %+v", after.Members)
	}
	h.expect(http.StatusOK, alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID}})
	h.mustDo(alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": true, "members": []string{bobUser.ID}}, &after)
	if len(after.Members) != 0 || !after.Shared {
		t.Fatalf("shared:true : membres %+v", after.Members)
	}
	// Des membres sans « shared » sur une liste partagée avec tous : des comptes précis.
	h.mustDo(alice, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID}}, &after)
	if after.Shared || len(after.Members) != 1 {
		t.Fatalf("members seul : shared=%v membres %+v", after.Shared, after.Members)
	}
}

// Compte membre supprimé : les autres lecteurs de la liste reçoivent les membres à jour.
func TestDeletedMemberEvent(t *testing.T) {
	h := newHarness(t)
	admin := h.session(h.addUser("alice"))
	bobUser, carolUser := h.addUser("bob"), h.addUser("carol")
	l := h.createList(admin, "Courses")
	h.expect(http.StatusOK, admin, "PATCH", "/api/lists/"+l.ID, map[string]any{"members": []string{bobUser.ID, carolUser.ID}})
	srv := httptest.NewServer(h.app.routes())
	t.Cleanup(srv.Close)
	evC := openSSE(t, srv.URL, h.session(carolUser))
	if w := h.form(admin, "/admin/users/"+bobUser.ID+"/delete", url.Values{}); w.Code != http.StatusSeeOther {
		t.Fatalf("suppression : %d", w.Code)
	}
	ev := evC.next(t, 2*time.Second)
	var got List
	json.Unmarshal([]byte(ev.Data), &got)
	if ev.Name != evList || got.ID != l.ID || len(got.Members) != 1 || got.Members[0].ID != carolUser.ID {
		t.Fatalf("carol : %s %s", ev.Name, ev.Data)
	}
}
