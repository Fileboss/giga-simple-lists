package main

// Couleurs des listes : 8 pastels, chacun en deux teintes. Deep, un cran plus sombre,
// est le fond de la liste ; Hex, la teinte claire, celui des catégories (et de la
// pastille). Le contraste du texte noir est ≥ 7:1 (WCAG AAA) sur chacune, celui du gris
// des éléments cochés ≥ 4,5:1 (vérifié par TestColorContrast, ainsi que celui de --ink,
// le texte réel). DarkHex et DarkDeep jouent les mêmes rôles en mode sombre, avec les
// mêmes seuils pour le texte clair. Les classes .c-<clé> de app.css doivent reprendre
// ces valeurs. Le nom affiché vient de i18n.go (color.<clé>).
type color struct {
	Key               string
	Hex, Deep         string
	DarkHex, DarkDeep string
}

var colors = []color{
	{"jaune", "#fcf1dc", "#f6e0b0", "#383221", "#2b2718"},
	{"peche", "#fbe9df", "#fbdccb", "#3a2c25", "#2d221d"},
	{"rose", "#f8e6ea", "#f5d5dc", "#3a2b30", "#2d2125"},
	{"lavande", "#ece9f6", "#e2ddf2", "#2f2c3c", "#24222f"},
	{"bleu", "#e5eef6", "#d5e4f2", "#27313d", "#1e2630"},
	{"menthe", "#e3f2ea", "#d4eddf", "#26362e", "#1d2a24"},
	{"sauge", "#ebf0e1", "#e2ead2", "#2e3626", "#232a1d"},
	{"gris", "#efefec", "#e6e6e1", "#31312e", "#262624"},
}

// Couleurs du texte (app.css : --ink) et des éléments cochés (--muted), en mode clair
// puis en mode sombre.
const (
	inkColor        = "#2e2e2c"
	checkedGray     = "#62625d"
	darkInkColor    = "#e9e7e1"
	darkCheckedGray = "#a8a69e"
)

func validColor(k string) bool {
	for _, c := range colors {
		if c.Key == k {
			return true
		}
	}
	return false
}
