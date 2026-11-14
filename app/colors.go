package main

// Couleurs des listes : 8 pastels, chacun en deux teintes. Deep, un cran plus sombre,
// est le fond de la liste ; Hex, la teinte claire, celui des catégories (et de la
// pastille). Le contraste du texte noir est ≥ 7:1 (WCAG AAA) sur chacune, celui du gris
// des éléments cochés ≥ 4,5:1 (vérifié par TestColorContrast, ainsi que celui de --ink,
// le texte réel). En mode sombre, fond de liste et catégories sont neutres (darkCategory,
// mêmes seuils de texte) et la couleur devient un accent : DarkAccent, vif, pour la
// pastille et les cases cochées (coche foncée ≥ 4,5:1 dessus). Les classes .c-<clé> de
// app.css doivent reprendre ces valeurs. Le nom affiché vient de i18n.go (color.<clé>).
type color struct {
	Key        string
	Hex, Deep  string
	DarkAccent string
}

var colors = []color{
	{"jaune", "#fcf1dc", "#f6e0b0", "#e3c66f"},
	{"peche", "#fbe9df", "#fbdccb", "#eba383"},
	{"rose", "#f8e6ea", "#f5d5dc", "#e795ad"},
	{"lavande", "#ece9f6", "#e2ddf2", "#ab9de6"},
	{"bleu", "#e5eef6", "#d5e4f2", "#80b4e6"},
	{"menthe", "#e3f2ea", "#d4eddf", "#72c9a3"},
	{"sauge", "#ebf0e1", "#e2ead2", "#a6bd8c"},
	{"gris", "#efefec", "#e6e6e1", "#b8b6ae"},
}

// Couleurs du texte (app.css : --ink) et des éléments cochés (--muted), en mode clair
// puis en mode sombre.
const (
	inkColor        = "#2e2e2c"
	checkedGray     = "#62625d"
	darkInkColor    = "#e9e7e1"
	darkCheckedGray = "#a8a69e"
	darkCheckMark   = "#1d1d1b" // coche (check-dark.svg) sur l'accent, en mode sombre
	darkCategory    = "#2e2e2b" // fond des catégories en mode sombre, plus clair que la liste
)

func validColor(k string) bool {
	for _, c := range colors {
		if c.Key == k {
			return true
		}
	}
	return false
}
