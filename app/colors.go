package main

// Couleurs de fond des listes : 8 pastels clairs. Le contraste du texte noir est ≥ 7:1
// (WCAG AAA) sur chacune, celui du gris des éléments cochés ≥ 4,5:1 (vérifié par
// TestColorContrast, ainsi que celui de --ink, le texte réel). Les classes .c-<clé> de app.css doivent reprendre ces valeurs.
type color struct {
	Key   string
	Label string
	Hex   string
}

var colors = []color{
	{"jaune", "Jaune", "#faf3dc"},
	{"peche", "Pêche", "#fbe9df"},
	{"rose", "Rose", "#f8e6ea"},
	{"lavande", "Lavande", "#ece9f6"},
	{"bleu", "Bleu", "#e5eef6"},
	{"menthe", "Menthe", "#e3f2ea"},
	{"sauge", "Sauge", "#ebf0e1"},
	{"gris", "Gris", "#efefec"},
}

// Couleurs du texte (app.css : --ink) et des éléments cochés (--muted).
const (
	inkColor    = "#2e2e2c"
	checkedGray = "#62625d"
)

func validColor(k string) bool {
	for _, c := range colors {
		if c.Key == k {
			return true
		}
	}
	return false
}
