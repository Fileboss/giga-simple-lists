package main

// Couleurs des listes : 8 pastels, chacun en deux teintes. Deep, un cran plus sombre,
// est le fond de la liste ; Hex, la teinte claire, celui des catégories (et de la
// pastille). Le contraste du texte noir est ≥ 7:1 (WCAG AAA) sur chacune, celui du gris
// des éléments cochés ≥ 4,5:1 (vérifié par TestColorContrast, ainsi que celui de --ink,
// le texte réel). Les classes .c-<clé> de app.css doivent reprendre ces valeurs.
type color struct {
	Key   string
	Label string
	Hex   string
	Deep  string
}

var colors = []color{
	{"jaune", "Jaune", "#fcf1dc", "#f6e0b0"},
	{"peche", "Pêche", "#fbe9df", "#fbdccb"},
	{"rose", "Rose", "#f8e6ea", "#f5d5dc"},
	{"lavande", "Lavande", "#ece9f6", "#e2ddf2"},
	{"bleu", "Bleu", "#e5eef6", "#d5e4f2"},
	{"menthe", "Menthe", "#e3f2ea", "#d4eddf"},
	{"sauge", "Sauge", "#ebf0e1", "#e2ead2"},
	{"gris", "Gris", "#efefec", "#e6e6e1"},
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
