package main

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const adminUsage = `usage :
  listes user add <nom> [--password-stdin]     crée un compte ; affiche le mot de passe généré
  listes user reset <nom> [--password-stdin]   nouveau mot de passe, sessions révoquées
  listes user list                             liste les comptes
  listes sessions revoke-all                   déconnecte tous les appareils de tous les comptes`

// adminCommand : gestion des comptes, en ligne de commande uniquement
// (docker compose exec listes listes user add <nom>).
func adminCommand(out io.Writer, in io.Reader, db *sql.DB, args []string) error {
	if len(args) < 2 {
		return errors.New(adminUsage)
	}
	switch args[0] + " " + args[1] {
	case "user add", "user reset":
		if len(args) < 3 {
			return errors.New(adminUsage)
		}
		fromStdin := len(args) > 3 && args[3] == "--password-stdin"
		return setUser(out, in, db, args[2], args[1] == "add", fromStdin)
	case "user list":
		rows, err := db.Query(`SELECT u.name, (SELECT count(*) FROM sessions s WHERE s.user_id = u.id) FROM users u ORDER BY u.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var n int
			if err := rows.Scan(&name, &n); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s\t%d session(s)\n", name, n)
		}
		return rows.Err()
	case "sessions revoke-all":
		res, err := db.Exec(`DELETE FROM sessions`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		fmt.Fprintf(out, "%d session(s) révoquée(s). Les flux temps réel ouverts se coupent sous 20 s.\n", n)
		return nil
	}
	return errors.New(adminUsage)
}

func validUserName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > maxUserNameRunes || !utf8.ValidString(name) {
		return fmt.Errorf("nom invalide : 1 à %d caractères", maxUserNameRunes)
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.", r) {
			return errors.New("nom invalide : lettres, chiffres, - _ . seulement")
		}
	}
	return nil
}

func setUser(out io.Writer, in io.Reader, db *sql.DB, name string, create, fromStdin bool) error {
	if err := validUserName(name); err != nil {
		return err
	}
	password := generatePassword()
	if fromStdin {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		password = strings.TrimRight(line, "\r\n")
		if utf8.RuneCountInString(password) < 15 {
			return errors.New("mot de passe trop court : 15 caractères minimum")
		}
	}
	hash := hashPassword(password)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if create {
		// NOCASE de SQLite ne couvre que l'ASCII : « zoé » et « ZOÉ » sont comparés ici.
		var taken bool
		if taken, err = nameTaken(tx, name); err == nil && taken {
			return fmt.Errorf("le compte %q existe déjà (user reset pour le réinitialiser)", name)
		}
		_, err = tx.Exec(`INSERT INTO users (id, name, password_hash, created_at) VALUES (?, ?, ?, ?)`,
			newID(), name, hash, nowMs())
		if isConstraint(err) {
			return fmt.Errorf("le compte %q existe déjà (user reset pour le réinitialiser)", name)
		}
	} else {
		var res sql.Result
		res, err = tx.Exec(`UPDATE users SET password_hash = ? WHERE name = ?`, hash, name)
		if n, _ := rowsAffected(res, err); err == nil && n != 1 {
			return fmt.Errorf("compte %q introuvable", name)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE name = ?)`, name)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM login_attempts WHERE key = ?`, userKey(name))
		}
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	verb := "créé"
	if !create {
		verb = "réinitialisé (sessions révoquées)"
	}
	fmt.Fprintf(out, "Compte %q %s.\n\n", name, verb)
	if !fromStdin {
		fmt.Fprintf(out, "Mot de passe (affiché une seule fois, à ranger dans un gestionnaire de mots de passe) :\n\n    %s\n\n", password)
	}
	return nil
}

func nameTaken(tx *sql.Tx, name string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM users`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return false, err
		}
		if strings.EqualFold(n, name) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// generatePassword : 6 groupes de 4 caractères sans ambiguïté (≈ 120 bits).
func generatePassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789" // 31 symboles
	var b strings.Builder
	buf := make([]byte, 1)
	for i := 0; i < 24; {
		rand.Read(buf)
		if int(buf[0]) >= 31*8 { // rejet : distribution uniforme
			continue
		}
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(alphabet[int(buf[0])%31])
		i++
	}
	return b.String()
}
