package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A ne peut ni lire, ni modifier, ni supprimer les listes privées de B, même avec leurs identifiants.
func TestPrivateListsInvisibleToOthers(t *testing.T) {
	h := newHarness(t)
	sa, sb := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	l := h.createList(sb, "Privée de Bob")
	it := h.createItem(sb, l.ID, "secret")

	if hasList(h.state(sa), l.ID) {
		t.Fatal("liste privée visible par un autre")
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/lists/" + l.ID, map[string]any{"name": "piratée"}},
		{"PATCH", "/api/lists/" + l.ID, map[string]any{"shared": true}},
		{"PATCH", "/api/lists/" + l.ID, map[string]any{"color": "rose"}},
		{"DELETE", "/api/lists/" + l.ID, nil},
		{"POST", "/api/lists/" + l.ID + "/items", map[string]any{"text": "intrus"}},
		{"POST", "/api/lists/" + l.ID + "/clear-checked", map[string]any{}},
		{"PATCH", "/api/items/" + it.ID, map[string]any{"checked": true}},
		{"PATCH", "/api/items/" + it.ID, map[string]any{"text": ""}},
		{"DELETE", "/api/items/" + it.ID, nil},
		// Identifiants devinés : même réponse que pour une liste existante.
		{"PATCH", "/api/lists/" + newID(), map[string]any{"name": "x"}},
		{"DELETE", "/api/items/" + newID(), nil},
		{"PATCH", "/api/lists/not-an-id", map[string]any{"name": "x"}},
	} {
		w := h.expect(http.StatusNotFound, sa, c.method, c.path, c.body)
		if w.Body.String() != "{\"error\":\"Introuvable.\"}\n" {
			t.Fatalf("%s %s : corps %q", c.method, c.path, w.Body.String())
		}
	}
	// Rien n'a bougé.
	var name string
	var checked bool
	h.db.QueryRow(`SELECT name FROM lists WHERE id = ?`, l.ID).Scan(&name)
	h.db.QueryRow(`SELECT checked FROM items WHERE id = ?`, it.ID).Scan(&checked)
	if name != "Privée de Bob" || checked {
		t.Fatal("liste privée modifiée par un autre")
	}
}

// Le list_id envoyé par le client n'est jamais utilisé : le droit sur un élément vient de sa liste.
func TestItemRightsComeFromItsList(t *testing.T) {
	h := newHarness(t)
	sa, sb := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	mine := h.createList(sa, "À Alice")
	theirs := h.createList(sb, "À Bob")
	it := h.createItem(sb, theirs.ID, "lait")
	h.expect(http.StatusBadRequest, sa, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true, "list_id": mine.ID})
	h.expect(http.StatusNotFound, sa, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true})
}

// Rejouer un nonce ou un identifiant d'un autre ne donne accès à rien.
func TestReplayedIdentifiers(t *testing.T) {
	h := newHarness(t)
	sa, sb := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	nonce := "0123456789abcdef0123456789abcdef"
	var la, lb List
	h.mustDo(sb, "POST", "/api/lists", map[string]any{"nonce": nonce, "name": "B"}, &lb)
	h.mustDo(sa, "POST", "/api/lists", map[string]any{"nonce": nonce, "name": "A"}, &la)
	if la.ID == lb.ID || la.OwnerID != sa.User.ID {
		t.Fatal("même nonce, même identifiant pour deux utilisateurs")
	}
	// Renvoi de la même création (file hors ligne) : idempotent.
	var again List
	h.mustDo(sa, "POST", "/api/lists", map[string]any{"nonce": nonce, "name": "A"}, &again)
	if again.ID != la.ID {
		t.Fatal("renvoi non idempotent")
	}
	var count int
	h.db.QueryRow(`SELECT count(*) FROM lists WHERE owner_id = ?`, sa.User.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("%d listes au lieu d'une", count)
	}
	// Élément : même nonce rejoué dans la même liste → même élément ; dans une autre liste → 404.
	var i1, i2 Item
	h.mustDo(sb, "POST", "/api/lists/"+lb.ID+"/items", map[string]any{"nonce": nonce, "text": "x"}, &i1)
	h.mustDo(sb, "POST", "/api/lists/"+lb.ID+"/items", map[string]any{"nonce": nonce, "text": "x"}, &i2)
	if i1.ID != i2.ID {
		t.Fatal("renvoi d'élément non idempotent")
	}
	other := h.createList(sb, "B2")
	h.expect(http.StatusNotFound, sb, "POST", "/api/lists/"+other.ID+"/items", map[string]any{"nonce": nonce, "text": "x"})
	h.expect(http.StatusBadRequest, sb, "POST", "/api/lists", map[string]any{"nonce": "court"})
}

// B ne peut ni changer le partage, ni la couleur, ni supprimer une liste partagée par A ;
// il peut la renommer et tout faire sur ses éléments.
func TestSharedListRights(t *testing.T) {
	h := newHarness(t)
	sa, sb := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	l := h.createList(sa, "Courses")
	it := h.createItem(sa, l.ID, "lait")
	h.mustDo(sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": true}, nil)

	st := h.state(sb)
	if !hasList(st, l.ID) {
		t.Fatal("liste partagée invisible")
	}
	h.expect(http.StatusForbidden, sb, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": false})
	h.expect(http.StatusForbidden, sb, "PATCH", "/api/lists/"+l.ID, map[string]any{"color": "rose"})
	h.expect(http.StatusForbidden, sb, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": "x", "color": "rose"})
	h.expect(http.StatusForbidden, sb, "DELETE", "/api/lists/"+l.ID, nil)

	h.expect(http.StatusOK, sb, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": "Courses de la semaine"})
	h.expect(http.StatusOK, sb, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true})
	h.expect(http.StatusOK, sb, "PATCH", "/api/items/"+it.ID, map[string]any{"text": "lait demi-écrémé"})
	added := h.createItem(sb, l.ID, "pain")
	h.expect(http.StatusOK, sb, "POST", "/api/lists/"+l.ID+"/clear-checked", map[string]any{})
	h.expect(http.StatusNoContent, sb, "DELETE", "/api/items/"+added.ID, nil)

	var shared bool
	var color, name string
	h.db.QueryRow(`SELECT shared, color, name FROM lists WHERE id = ?`, l.ID).Scan(&shared, &color, &name)
	if !shared || color != colors[0].Key || name != "Courses de la semaine" {
		t.Fatalf("état inattendu : %v %s %s", shared, color, name)
	}
	// Le propriétaire, lui, peut tout faire.
	h.expect(http.StatusOK, sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"color": "rose"})
	h.expect(http.StatusNoContent, sa, "DELETE", "/api/lists/"+l.ID, nil)
}

// Une liste qui redevient privée cesse immédiatement d'être accessible à B.
func TestUnsharedListImmediatelyInaccessible(t *testing.T) {
	h := newHarness(t)
	sa, sb := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	l := h.createList(sa, "Courses")
	it := h.createItem(sa, l.ID, "lait")
	h.mustDo(sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": true}, nil)
	h.expect(http.StatusOK, sb, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true})

	h.mustDo(sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": false}, nil)
	if hasList(h.state(sb), l.ID) {
		t.Fatal("liste encore visible")
	}
	h.expect(http.StatusNotFound, sb, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": "x"})
	h.expect(http.StatusNotFound, sb, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": false})
	h.expect(http.StatusNotFound, sb, "POST", "/api/lists/"+l.ID+"/items", map[string]any{"text": "x"})
	h.expect(http.StatusNotFound, sb, "POST", "/api/lists/"+l.ID+"/clear-checked", map[string]any{})
}

// Le flux SSE : B ne reçoit rien des listes privées de A ; il voit apparaître la liste
// partagée, ses modifications, puis seulement un retrait sans contenu quand elle redevient privée.
func TestSSEFilteredPerUser(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer(h.app.routes())
	t.Cleanup(srv.Close) // après la fermeture des flux (les Cleanup s'exécutent en ordre inverse)
	sa, sb := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	evA, evB := openSSE(t, srv.URL, sa), openSSE(t, srv.URL, sb)

	l := h.createList(sa, "Courses")
	if ev := evA.next(t, time.Second); ev.Name != "list" {
		t.Fatalf("A : %v", ev)
	}
	it := h.createItem(sa, l.ID, "lait")
	h.mustDo(sa, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": true}, nil)
	h.mustDo(sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"name": "Courses secrètes"}, nil)
	evB.none(t, 300*time.Millisecond)

	// Partage : la liste arrive chez B, avec ses éléments, en moins d'une seconde.
	start := time.Now()
	h.mustDo(sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": true}, nil)
	ev := evB.next(t, time.Second)
	var got List
	json.Unmarshal([]byte(ev.Data), &got)
	if ev.Name != "list" || got.ID != l.ID || len(got.Items) != 1 || time.Since(start) > time.Second {
		t.Fatalf("partage : %v", ev)
	}
	// Une coche de A arrive chez B.
	h.mustDo(sa, "PATCH", "/api/items/"+it.ID, map[string]any{"checked": false}, nil)
	if ev := evB.next(t, time.Second); ev.Name != "item" {
		t.Fatalf("coche : %v", ev)
	}

	// Retour en privé : B ne reçoit qu'un retrait, sans contenu.
	h.mustDo(sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": false}, nil)
	ev = evB.next(t, time.Second)
	var rm map[string]any
	json.Unmarshal([]byte(ev.Data), &rm)
	if ev.Name != "list-removed" || len(rm) != 2 || rm["id"] != l.ID || rm["reason"] != "unshared" {
		t.Fatalf("retrait : %v", ev)
	}
	// Ensuite, plus rien.
	h.mustDo(sa, "PATCH", "/api/items/"+it.ID, map[string]any{"text": "lait entier"}, nil)
	h.createItem(sa, l.ID, "beurre")
	h.mustDo(sa, "DELETE", "/api/lists/"+l.ID, nil, nil)
	evB.none(t, 300*time.Millisecond)
}

// Réordonner : dans la même liste uniquement, avec les droits de la liste.
func TestMoveItem(t *testing.T) {
	h := newHarness(t)
	sa, sb := h.session(h.addUser("alice")), h.session(h.addUser("bob"))
	l := h.createList(sa, "Courses")
	a, b, c := h.createItem(sa, l.ID, "a"), h.createItem(sa, l.ID, "b"), h.createItem(sa, l.ID, "c")
	order := func() string {
		var s string
		for _, it := range h.state(sa).Lists[0].Items {
			s += it.Text
		}
		return s
	}
	h.expect(http.StatusOK, sa, "POST", "/api/items/"+c.ID+"/move", map[string]any{"before": a.ID})
	if o := order(); o != "cab" {
		t.Fatalf("ordre : %s", o)
	}
	h.expect(http.StatusOK, sa, "POST", "/api/items/"+c.ID+"/move", map[string]any{"before": ""})
	if o := order(); o != "abc" {
		t.Fatalf("ordre : %s", o)
	}
	// Rejouer le même déplacement ne change rien.
	h.expect(http.StatusOK, sa, "POST", "/api/items/"+c.ID+"/move", map[string]any{"before": ""})
	if o := order(); o != "abc" {
		t.Fatalf("ordre : %s", o)
	}
	// Cible dans une autre liste (privée de B) ou inexistante : refusé, rien ne bouge.
	other := h.createList(sb, "B")
	x := h.createItem(sb, other.ID, "x")
	h.expect(http.StatusBadRequest, sa, "POST", "/api/items/"+a.ID+"/move", map[string]any{"before": x.ID})
	h.expect(http.StatusBadRequest, sa, "POST", "/api/items/"+a.ID+"/move", map[string]any{"before": newID()})
	h.expect(http.StatusBadRequest, sa, "POST", "/api/items/"+a.ID+"/move", map[string]any{"before": a.ID})
	h.expect(http.StatusBadRequest, sa, "POST", "/api/items/"+a.ID+"/move", map[string]any{"before": "pas-un-id"})
	if o := order(); o != "abc" {
		t.Fatalf("ordre : %s", o)
	}
	// B ne peut pas réordonner la liste privée de A ; il le peut une fois partagée.
	h.expect(http.StatusNotFound, sb, "POST", "/api/items/"+b.ID+"/move", map[string]any{"before": a.ID})
	h.mustDo(sa, "PATCH", "/api/lists/"+l.ID, map[string]any{"shared": true}, nil)
	h.expect(http.StatusOK, sb, "POST", "/api/items/"+b.ID+"/move", map[string]any{"before": a.ID})
	if o := order(); o != "bac" {
		t.Fatalf("ordre : %s", o)
	}
}
