package main

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// adminCommand : gestion des comptes, en ligne de commande uniquement
// (docker compose exec listes listes user add <nom>).
func adminCommand(out io.Writer, in io.Reader, db *sql.DB, args []string) error {
	if len(args) < 2 {
		return errors.New(T("cli.usage"))
	}
	switch args[0] + " " + args[1] {
	case "user add", "user reset":
		if len(args) < 3 {
			return errors.New(T("cli.usage"))
		}
		fromStdin := len(args) > 3 && args[3] == "--password-stdin"
		return setUser(out, in, db, args[2], args[1] == "add", fromStdin)
	case "user admin":
		// Arguments stricts : une faute de frappe sur --revoke ne doit pas donner le rôle.
		if len(args) < 3 || len(args) > 4 || (len(args) == 4 && args[3] != "--revoke") {
			return errors.New(T("cli.usage"))
		}
		revoke := len(args) == 4
		u, err := userByName(db, args[2])
		if errors.Is(err, errNotFound) {
			return fmt.Errorf(T("cli.notFound"), args[2])
		}
		if err != nil {
			return err
		}
		if err := inTx(db, func(tx *sql.Tx) error { return setAdmin(tx, u.ID, !revoke) }); errors.Is(err, errLastAdmin) {
			return errors.New(T("cli.lastAdmin"))
		} else if err != nil {
			return err
		}
		if revoke {
			fmt.Fprintf(out, T("cli.adminRemoved"), u.Name)
		} else {
			fmt.Fprintf(out, T("cli.adminSet"), u.Name)
		}
		return nil
	case "user list":
		rows, err := db.Query(`SELECT u.name, u.is_admin, (SELECT count(*) FROM sessions s WHERE s.user_id = u.id) FROM users u ORDER BY u.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			var admin bool
			var n int
			if err := rows.Scan(&name, &admin, &n); err != nil {
				return err
			}
			if admin {
				name += " " + T("cli.adminTag")
			}
			fmt.Fprintf(out, T("cli.sessionsRow"), name, n)
		}
		return rows.Err()
	case "sessions revoke-all":
		res, err := db.Exec(`DELETE FROM sessions`)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		fmt.Fprintf(out, T("cli.revoked"), n)
		return nil
	}
	return errors.New(T("cli.usage"))
}

func validUserName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > maxUserNameRunes || !utf8.ValidString(name) {
		return fmt.Errorf(T("cli.nameLength"), maxUserNameRunes)
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("-_.", r) {
			return errors.New(T("cli.nameChars"))
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
		if err := validPassword(password); err != nil {
			return err
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
		if taken, err = nameTaken(tx, name); err != nil {
			return err
		}
		if taken {
			return fmt.Errorf(T("cli.exists"), name)
		}
		// Le premier compte est admin : une instance a toujours quelqu'un pour gérer les comptes.
		var n int
		if err = tx.QueryRow(`SELECT count(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO users (id, name, password_hash, created_at, is_admin) VALUES (?, ?, ?, ?, ?)`,
			newID(), name, hash, nowMs(), n == 0)
		if isConstraint(err) {
			return fmt.Errorf(T("cli.exists"), name)
		}
		if err == nil && n == 0 {
			// Premier compte créé ici : le lien de premier compte écrit dans les journaux ne sert plus.
			_, err = tx.Exec(`DELETE FROM invitations WHERE kind = 'invite' AND created_by IS NULL AND is_admin = 1`)
		}
	} else {
		var res sql.Result
		res, err = tx.Exec(`UPDATE users SET password_hash = ? WHERE name = ?`, hash, name)
		if n, _ := rowsAffected(res, err); err == nil && n != 1 {
			return fmt.Errorf(T("cli.notFound"), name)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM sessions WHERE user_id = (SELECT id FROM users WHERE name = ?)`, name)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM api_tokens WHERE user_id = (SELECT id FROM users WHERE name = ?)`, name)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM login_attempts WHERE key = ?`, userKey(name))
		}
		if err == nil { // un lien de réinitialisation en attente ne sert plus
			_, err = tx.Exec(`DELETE FROM invitations WHERE kind = 'reset' AND user_id = (SELECT id FROM users WHERE name = ?)`, name)
		}
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if create {
		fmt.Fprintf(out, T("cli.created"), name)
	} else {
		fmt.Fprintf(out, T("cli.reset"), name)
	}
	if !fromStdin {
		fmt.Fprintf(out, T("cli.passwordOnce"), password)
	}
	return nil
}

// inviteCommand : lien d'invitation à usage unique, sans passer par la page
// d'administration (instance sans admin joignable, script…).
func inviteCommand(out io.Writer, db *sql.DB, origin string, admin bool) error {
	if origin == "" {
		return errors.New(T("err.origin"))
	}
	token, err := createInvitation(db, kindInvite, admin, "", "", inviteTTL)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, T("cli.inviteLink"), origin+"/invite/"+token)
	return nil
}

// backup : copie cohérente de la base, même pendant que le serveur tourne (VACUUM INTO
// lit un instantané, le WAL laisse le serveur écrire). L'image n'a pas de sqlite3 : sans
// cette commande, il faudrait l'installer sur l'hôte. Par défaut, la copie va à côté de
// la base, donc dans le volume /data. Un fichier existant n'est jamais écrasé.
func backup(out io.Writer, db *sql.DB, dbPath, dest string) error {
	if dest == "" {
		dest = filepath.Join(filepath.Dir(dbPath), "listes-backup-"+now().Format("20060102-150405")+".db")
	} else if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(dbPath), dest) // / est en lecture seule dans l'image
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf(T("cli.backupExists"), dest)
	}
	if _, err := db.Exec(`VACUUM INTO ?`, dest); err != nil {
		return err
	}
	st, err := os.Stat(dest)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, T("cli.backupDone"), dest, st.Size())
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

func rowsAffected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// inTx : fn dans une transaction (BEGIN IMMEDIATE : sérialisée avec le serveur).
func inTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
