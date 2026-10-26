package main

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Taille maximale de la base : 50 Mio (pages de 4 Kio). Au-delà, SQLite refuse les
// écritures (SQLITE_FULL) : l'API répond 507, la lecture continue de marcher. Le disque
// de l'hôte, souvent partagé avec d'autres services, ne peut pas être rempli par Listes.
var dbMaxPages = 50 << 20 / 4096

// isStorageFull : écriture refusée par la limite de taille (ou disque plein).
func isStorageFull(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_FULL
}

// openDB ouvre la base en WAL, clés étrangères activées, et applique les migrations
// intégrées au binaire (numérotées, suivies par PRAGMA user_version).
func openDB(path string) (*sql.DB, error) {
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "foreign_keys(1)", "busy_timeout(10000)",
		"synchronous(NORMAL)", "temp_store(MEMORY)", "cache_size(-2000)", fmt.Sprintf("max_page_count(%d)", dbMaxPages)} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// Quelques utilisateurs : une seule connexion suffit, sérialise les écritures et garde la RAM basse.
	db.SetMaxOpenConns(1)
	db.SetConnMaxIdleTime(0)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for i, name := range names {
		if i < version {
			continue
		}
		body, err := migrationsFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s : %w", name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		return fmt.Errorf("clés étrangères inactives")
	}
	return nil
}

// newID : 128 bits aléatoires, en hexadécimal.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// now est remplaçable dans les tests (expiration des sessions, temporisations).
var now = time.Now

func nowMs() int64 { return now().UnixMilli() }
