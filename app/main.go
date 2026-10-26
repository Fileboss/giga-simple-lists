// Listes : listes à cocher partagées, temps réel (SSE), un seul binaire.
//
//	listes [serve]                 serveur HTTP
//	listes user add <nom>          crée un compte (mot de passe généré)
//	listes user reset <nom>        nouveau mot de passe, sessions révoquées
//	listes user list               comptes existants
//	listes sessions revoke-all     déconnecte tout le monde
//	listes healthcheck             pour le healthcheck Docker (pas de shell ni de wget dans l'image)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type config struct {
	Addr           string   // LISTES_ADDR
	DBPath         string   // LISTES_DB
	Origin         string   // LISTES_ORIGIN : seule origine acceptée pour les requêtes qui modifient
	TrustedProxies []string // LISTES_TRUSTED_PROXIES : IP dont on lit X-Forwarded-For
}

func loadConfig() config {
	c := config{
		Addr:           env("LISTES_ADDR", ":5240"),
		DBPath:         env("LISTES_DB", "/data/listes.db"),
		Origin:         strings.TrimRight(os.Getenv("LISTES_ORIGIN"), "/"),
		TrustedProxies: []string{"127.0.0.1"},
	}
	for _, ip := range strings.Split(os.Getenv("LISTES_TRUSTED_PROXIES"), ",") {
		if ip = strings.TrimSpace(ip); net.ParseIP(ip) != nil {
			c.TrustedProxies = append(c.TrustedProxies, ip)
		}
	}
	return c
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "erreur :", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg := loadConfig()
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serve(cfg)
	case "healthcheck":
		return healthcheck(cfg)
	case "user", "sessions":
		db, err := openDB(cfg.DBPath)
		if err != nil {
			return err
		}
		defer db.Close()
		return adminCommand(os.Stdout, os.Stdin, db, args)
	default:
		return fmt.Errorf("commande inconnue %q (serve, user, sessions, healthcheck)", cmd)
	}
}

func serve(cfg config) error {
	if !strings.HasPrefix(cfg.Origin, "https://") && !strings.HasPrefix(cfg.Origin, "http://localhost") {
		return errors.New("LISTES_ORIGIN doit être l'URL publique de l'appli, par exemple https://listes.example.com (ou http://localhost:5240 en local)")
	}
	db, err := openDB(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	app := newApp(cfg, db)
	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: app.routes(),
		// WriteTimeout est levé par le flux SSE pour sa seule connexion (voir events).
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go app.housekeeping(ctx)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("listes démarré", "addr", cfg.Addr, "origin", cfg.Origin)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	app.hub.closeAll()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func healthcheck(cfg config) error {
	_, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return err
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz : %s", resp.Status)
	}
	return nil
}
