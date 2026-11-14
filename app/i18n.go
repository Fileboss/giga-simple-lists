package main

// Langue de l'interface, des messages d'erreur, de la ligne de commande et des journaux :
// une seule par instance, choisie par LISTES_LANG (en par défaut, ou fr). Rien dans
// l'interface ne permet de la changer.

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

const defaultLang = "en"

// lang : fixée au démarrage (setLang), avant toute requête.
var lang = defaultLang

func setLang(l string) error {
	l = strings.ToLower(strings.TrimSpace(l))
	if l == "" {
		l = defaultLang
	}
	if _, ok := messages[l]; !ok {
		return fmt.Errorf("LISTES_LANG=%q: unsupported language (%s)", l, strings.Join(slices.Sorted(maps.Keys(messages)), ", "))
	}
	lang = l
	return nil
}

// T : le message de la clé dans la langue de l'instance (la clé elle-même si elle manque,
// ce que TestMessagesComplete interdit).
func T(key string) string {
	if s, ok := messages[lang][key]; ok {
		return s
	}
	return key
}

func tf(key string, args ...any) string { return fmt.Sprintf(T(key), args...) }

// jsMessages : les messages « js.* », sans le préfixe, que app.js lit dans la page.
func jsMessages() map[string]string {
	out := map[string]string{}
	for k, v := range messages[lang] {
		if after, ok := strings.CutPrefix(k, "js."); ok {
			out[after] = v
		}
	}
	return out
}

// Chaque langue a exactement les mêmes clés (TestMessagesComplete). Dans les messages
// « js.* », {nom} est remplacé par app.js.
var messages = map[string]map[string]string{
	"en": {
		"app.name":        "Lists",
		"app.description": "Shared checklists",
		"list.default":    "New list",

		"color.jaune":   "Yellow",
		"color.peche":   "Peach",
		"color.rose":    "Pink",
		"color.lavande": "Lavender",
		"color.bleu":    "Blue",
		"color.menthe":  "Mint",
		"color.sauge":   "Sage",
		"color.gris":    "Grey",

		"page.login":     "Lists · sign in",
		"login.name":     "Name",
		"login.password": "Password",
		"login.submit":   "Sign in",
		"login.failed":   "Incorrect name or password.",
		"login.expired":  "This page has expired, please try again.",
		"login.wrongOrigin": "This server only accepts sign-ins at %s. Open the app at that address, " +
			"or set LISTES_ORIGIN to the address you are using.",

		"ui.prevLists":      "Previous lists",
		"ui.lists":          "Lists",
		"ui.nextLists":      "Next lists",
		"ui.undo":           "Undo",
		"ui.offline":        "Offline · changes will be sent when back online",
		"ui.switchList":     "Switch list",
		"ui.listName":       "List name",
		"ui.share":          "Share",
		"ui.addPlaceholder": "Add…",
		"ui.addLabel":       "Add an item",
		"ui.add":            "Add",
		"ui.uncheckAll":     "Uncheck all",
		"ui.clearChecked":   "Delete checked",
		"ui.color":          "Colour",
		"ui.deleteList":     "Delete list…",
		"ui.delete":         "Delete",
		"ui.cancel":         "Cancel",
		"ui.close":          "Close",
		"ui.newList":        "+ New list",
		"ui.rename":         "Rename",
		"ui.empty":          "No lists yet. Create the first one with the + button.",
		"ui.logout":         "Sign out",

		"js.quote":           "“{s}”",
		"js.itemsDeleted":    "{n} items deleted",
		"js.itemDeleted":     "{q} deleted",
		"js.categoryDeleted": "{q} deleted",
		"js.listDeleted":     "List {q} deleted",
		"js.listGone":        "This list is no longer available.",
		"js.refused":         "Change refused.",
		"js.unshared":        "This list is no longer shared",
		"js.deleted":         "This list has been deleted",
		"js.switcherLabel":   "{name}: switch list",
		"js.ownerNote":       "{owner}’s list, shared with you.",
		"js.from":            "from {owner}",
		"js.shared":          "Shared",
		"js.sharedAria":      "shared",
		"js.sharedWithMe":    "Shared with me",
		"js.todo":            "{n} to do",
		"js.otherOne":        "{n} more",
		"js.otherMany":       "{n} more",
		"js.seeAbove":        "Show {label} above",
		"js.seeBelow":        "Show {label} below",
		"js.categoryName":    "Category name",
		"js.itemText":        "Item text",
		"js.uncheckAll":      "Uncheck all",
		"js.checkAll":        "Check all",
		"js.move":            "Move (up and down arrows)",
		"js.check":           "Check",
		"js.remove":          "Remove this item",
		"js.confirmDelete":   "Delete {q} and all its items?",

		"err.notFound":       "Not found.",
		"err.forbidden":      "Only the list owner can do this.",
		"err.limit":          "Limit reached.",
		"err.tooLarge":       "Request too large.",
		"err.storageFull":    "Storage full: remove items or lists to continue.",
		"err.internal":       "Internal error.",
		"err.sessionExpired": "Session expired, please sign in again.",
		"err.refused":        "Request refused.",
		"err.tooManyStreams": "Too many open connections.",
		"err.contentType":    "Expected Content-Type: application/json",
		"err.badJSON":        "Invalid JSON",
		"err.nonce":          "invalid nonce",
		"err.nonceMissing":   "missing nonce",
		"err.text":           "invalid text",
		"err.textTooLong":    "text too long",
		"err.textEmpty":      "empty text",
		"err.nameEmpty":      "empty name",
		"err.color":          "unknown colour",
		"err.categoryCheck":  "a category cannot be checked",
		"err.position":       "invalid position",
		"err.tooManyItems":   "too many items",
		"err.item":           "invalid item",

		"log.started":             "listes started",
		"log.skipped":             "skipped",
		"log.loginRefusedCSRF":    "sign-in refused: invalid origin or CSRF token",
		"log.loginQueueFull":      "sign-in refused: too many slowed-down attempts waiting",
		"log.loginBlockedIP":      "sign-in refused: IP blocked (too many attempts)",
		"log.loginRefused":        "sign-in refused",
		"log.loginOK":             "sign-in successful",
		"log.cause":               "cause",
		"log.causePasswordLong":   "password too long",
		"log.causeUnknownAccount": "unknown account",
		"log.causePassword":       "wrong password",
		"log.purgeSessions":       "session purge",
		"log.purgeAttempts":       "attempt purge",
		"log.storageFull":         "database full",
		"log.request":             "request",
		"log.sessionCreate":       "session creation",
		"log.logout":              "sign-out",

		"cli.error": "error:",
		"cli.usage": `usage:
  listes user add <name> [--password-stdin]     create an account; prints the generated password
  listes user reset <name> [--password-stdin]   new password, sessions revoked
  listes user list                              list accounts
  listes sessions revoke-all                    sign out every device of every account
  listes backup [<file>]                        consistent copy of the database (default: next to it)`,
		"cli.unknownCommand": "unknown command %q (serve, user, sessions, backup, healthcheck)",
		"cli.nameLength":     "invalid name: 1 to %d characters",
		"cli.nameChars":      "invalid name: letters, digits, - _ . only",
		"cli.passwordShort":  "password too short: 15 characters minimum",
		"cli.exists":         "account %q already exists (use user reset to reset it)",
		"cli.notFound":       "account %q not found",
		"cli.created":        "Account %q created.\n\n",
		"cli.reset":          "Account %q reset (sessions revoked).\n\n",
		"cli.passwordOnce":   "Password (shown only once, store it in a password manager):\n\n    %s\n\n",
		"cli.sessionsRow":    "%s\t%d session(s)\n",
		"cli.revoked":        "%d session(s) revoked. Open real-time streams close within 20 s.\n",
		"cli.backupDone":     "Backup written to %s (%d bytes).\n",
		"cli.backupExists":   "%s already exists: choose another file",
		"err.origin":         "LISTES_ORIGIN must be the public URL of the app, for example https://lists.example.com (or http://localhost:5240 locally)",
		"err.originHTTP":     "LISTES_ORIGIN is %s: over plain HTTP, browsers drop secure session cookies and sign-in fails. Serve the app over HTTPS behind a reverse proxy, or, on a trusted local network only, set LISTES_INSECURE_HTTP=true",
		"err.insecureHTTPS":  "LISTES_INSECURE_HTTP only applies to an http:// LISTES_ORIGIN: remove it (cookies would lose their Secure flag)",
		"log.insecureHTTP":   "LISTES_INSECURE_HTTP: session cookies are sent over plain HTTP, use on a trusted local network only",
	},
	"fr": {
		"app.name":        "Listes",
		"app.description": "Listes à cocher partagées",
		"list.default":    "Nouvelle liste",

		"color.jaune":   "Jaune",
		"color.peche":   "Pêche",
		"color.rose":    "Rose",
		"color.lavande": "Lavande",
		"color.bleu":    "Bleu",
		"color.menthe":  "Menthe",
		"color.sauge":   "Sauge",
		"color.gris":    "Gris",

		"page.login":     "Listes · connexion",
		"login.name":     "Nom",
		"login.password": "Mot de passe",
		"login.submit":   "Se connecter",
		"login.failed":   "Identifiant ou mot de passe incorrect.",
		"login.expired":  "Page expirée, veuillez réessayer.",
		"login.wrongOrigin": "Ce serveur n’accepte les connexions qu’à l’adresse %s. Ouvrez l’appli à cette adresse, " +
			"ou réglez LISTES_ORIGIN sur l’adresse utilisée.",

		"ui.prevLists":      "Listes précédentes",
		"ui.lists":          "Listes",
		"ui.nextLists":      "Listes suivantes",
		"ui.undo":           "Annuler",
		"ui.offline":        "Hors ligne · envoi au retour du réseau",
		"ui.switchList":     "Changer de liste",
		"ui.listName":       "Nom de la liste",
		"ui.share":          "Partager",
		"ui.addPlaceholder": "Ajouter…",
		"ui.addLabel":       "Ajouter un élément",
		"ui.add":            "Ajouter",
		"ui.uncheckAll":     "Tout décocher",
		"ui.clearChecked":   "Supprimer les cochés",
		"ui.color":          "Couleur",
		"ui.deleteList":     "Supprimer la liste…",
		"ui.delete":         "Supprimer",
		"ui.cancel":         "Annuler",
		"ui.close":          "Fermer",
		"ui.newList":        "+ Nouvelle liste",
		"ui.rename":         "Renommer",
		"ui.empty":          "Aucune liste. Crée la première avec le bouton +.",
		"ui.logout":         "Se déconnecter",

		"js.quote":           "« {s} »",
		"js.itemsDeleted":    "{n} éléments supprimés",
		"js.itemDeleted":     "{q} supprimé",
		"js.categoryDeleted": "{q} supprimée",
		"js.listDeleted":     "Liste {q} supprimée",
		"js.listGone":        "Cette liste n’est plus là.",
		"js.refused":         "Modification refusée.",
		"js.unshared":        "Cette liste n’est plus partagée",
		"js.deleted":         "Cette liste a été supprimée",
		"js.switcherLabel":   "{name} : changer de liste",
		"js.ownerNote":       "Liste de {owner}, partagée avec toi.",
		"js.from":            "de {owner}",
		"js.shared":          "Partagée",
		"js.sharedAria":      "partagée",
		"js.sharedWithMe":    "Partagées avec moi",
		"js.todo":            "{n} à faire",
		"js.otherOne":        "{n} autre",
		"js.otherMany":       "{n} autres",
		"js.seeAbove":        "Voir {label} au-dessus",
		"js.seeBelow":        "Voir {label} en dessous",
		"js.categoryName":    "Nom de la catégorie",
		"js.itemText":        "Texte de l’élément",
		"js.uncheckAll":      "Tout décocher",
		"js.checkAll":        "Tout cocher",
		"js.move":            "Déplacer (flèches haut et bas)",
		"js.check":           "Cocher",
		"js.remove":          "Retirer cet élément",
		"js.confirmDelete":   "Supprimer {q} et tous ses éléments ?",

		"err.notFound":       "Introuvable.",
		"err.forbidden":      "Réservé au propriétaire de la liste.",
		"err.limit":          "Limite atteinte.",
		"err.tooLarge":       "Requête trop grosse.",
		"err.storageFull":    "Stockage plein : retire des éléments ou des listes pour continuer.",
		"err.internal":       "Erreur interne.",
		"err.sessionExpired": "Session expirée, reconnecte-toi.",
		"err.refused":        "Requête refusée.",
		"err.tooManyStreams": "Trop de connexions ouvertes.",
		"err.contentType":    "Content-Type attendu : application/json",
		"err.badJSON":        "JSON invalide",
		"err.nonce":          "nonce invalide",
		"err.nonceMissing":   "nonce manquant",
		"err.text":           "texte invalide",
		"err.textTooLong":    "texte trop long",
		"err.textEmpty":      "texte vide",
		"err.nameEmpty":      "nom vide",
		"err.color":          "couleur inconnue",
		"err.categoryCheck":  "une catégorie ne se coche pas",
		"err.position":       "position invalide",
		"err.tooManyItems":   "trop d'éléments",
		"err.item":           "élément invalide",

		"log.started":             "listes démarré",
		"log.skipped":             "sautés",
		"log.loginRefusedCSRF":    "connexion refusée : origine ou jeton CSRF invalide",
		"log.loginQueueFull":      "connexion refusée : trop de tentatives ralenties en attente",
		"log.loginBlockedIP":      "connexion refusée : IP bloquée (trop de tentatives)",
		"log.loginRefused":        "connexion refusée",
		"log.loginOK":             "connexion réussie",
		"log.cause":               "cause",
		"log.causePasswordLong":   "mot de passe trop long",
		"log.causeUnknownAccount": "compte inconnu",
		"log.causePassword":       "mot de passe",
		"log.purgeSessions":       "purge des sessions",
		"log.purgeAttempts":       "purge des tentatives",
		"log.storageFull":         "base pleine",
		"log.request":             "requête",
		"log.sessionCreate":       "création de session",
		"log.logout":              "déconnexion",

		"cli.error": "erreur :",
		"cli.usage": `usage :
  listes user add <nom> [--password-stdin]     crée un compte ; affiche le mot de passe généré
  listes user reset <nom> [--password-stdin]   nouveau mot de passe, sessions révoquées
  listes user list                             liste les comptes
  listes sessions revoke-all                   déconnecte tous les appareils de tous les comptes
  listes backup [<fichier>]                    copie cohérente de la base (par défaut : à côté d'elle)`,
		"cli.unknownCommand": "commande inconnue %q (serve, user, sessions, backup, healthcheck)",
		"cli.nameLength":     "nom invalide : 1 à %d caractères",
		"cli.nameChars":      "nom invalide : lettres, chiffres, - _ . seulement",
		"cli.passwordShort":  "mot de passe trop court : 15 caractères minimum",
		"cli.exists":         "le compte %q existe déjà (user reset pour le réinitialiser)",
		"cli.notFound":       "compte %q introuvable",
		"cli.created":        "Compte %q créé.\n\n",
		"cli.reset":          "Compte %q réinitialisé (sessions révoquées).\n\n",
		"cli.passwordOnce":   "Mot de passe (affiché une seule fois, à ranger dans un gestionnaire de mots de passe) :\n\n    %s\n\n",
		"cli.sessionsRow":    "%s\t%d session(s)\n",
		"cli.revoked":        "%d session(s) révoquée(s). Les flux temps réel ouverts se coupent sous 20 s.\n",
		"cli.backupDone":     "Sauvegarde écrite dans %s (%d octets).\n",
		"cli.backupExists":   "%s existe déjà : choisis un autre fichier",
		"err.origin":         "LISTES_ORIGIN doit être l'URL publique de l'appli, par exemple https://listes.example.com (ou http://localhost:5240 en local)",
		"err.originHTTP":     "LISTES_ORIGIN vaut %s : en HTTP simple, les navigateurs refusent les cookies de session sécurisés et la connexion échoue. Sers l'appli en HTTPS derrière un reverse proxy, ou, sur un réseau local de confiance uniquement, ajoute LISTES_INSECURE_HTTP=true",
		"err.insecureHTTPS":  "LISTES_INSECURE_HTTP ne s'applique qu'à une LISTES_ORIGIN en http:// : retire-la (les cookies perdraient leur attribut Secure)",
		"log.insecureHTTP":   "LISTES_INSECURE_HTTP : les cookies de session circulent en HTTP simple, à réserver à un réseau local de confiance",
	},
}
