// Command server runs Mail: the IMAP synchronization, the JSON API and the
// embedded web app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // time zones without relying on the system database

	"github.com/bhr-tools/mail/internal/api"
	"github.com/bhr-tools/mail/internal/config"
	"github.com/bhr-tools/mail/internal/db"
	"github.com/bhr-tools/mail/internal/demo"
	"github.com/bhr-tools/mail/internal/imapsync"
	"github.com/bhr-tools/mail/internal/imgproxy"
	"github.com/bhr-tools/mail/web"
)

// Each flag defaults to its environment variable, so the binary is configured
// with flags (NixOS module) or environment (container) alike.
func main() {
	host := flag.String("host", envOr("HOST", "127.0.0.1"), "listen address (env HOST)")
	port := flag.Int("port", envInt("PORT", 8080), "listen port (env PORT)")
	dataDir := flag.String("data-dir", envOr("DATA_DIR", "./data"), "directory of the database (env DATA_DIR)")
	dbPath := flag.String("db", os.Getenv("DB_PATH"), "database file, overrides -data-dir (env DB_PATH)")
	accountsPath := flag.String("accounts", os.Getenv("ACCOUNTS"), "accounts file, JSON (env ACCOUNTS)")
	noAuth := flag.Bool("no-auth", envBool("NO_AUTH"), "disable the master password (env NO_AUTH=true)")
	window := flag.Duration("window", envDuration("WINDOW", 365*24*time.Hour), "how far back the archive, sent mail and trash are copied (env WINDOW)")
	demoMode := flag.Bool("demo", envBool("DEMO"), "serve sample mail from servers in memory, without a real account (env DEMO=true)")
	flag.Parse()

	if *port < 1 || *port > 65535 {
		log.Fatalf("invalid port %d", *port)
	}
	path := *dbPath
	if path == "" {
		path = filepath.Join(*dataDir, "mail.db")
	}

	var accounts []*config.Account
	switch {
	case *demoMode:
		srv, err := demo.Start()
		if err != nil {
			log.Fatalf("demo: %v", err)
		}
		if err := srv.Seed(time.Now()); err != nil {
			log.Fatalf("demo: %v", err)
		}
		accounts = []*config.Account{srv.Account("demo", "Demo")}
	case *accountsPath != "":
		cfg, err := config.Load(*accountsPath)
		if err != nil {
			log.Fatalf("accounts: %v", err)
		}
		accounts = cfg.Accounts
	default:
		log.Fatal("no accounts: pass -accounts accounts.json (or -demo to try the app)")
	}

	store, err := db.Open(path)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := imapsync.New(store, accounts, *window)
	manager.Start(ctx)

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	server := &http.Server{
		Addr:              addr,
		Handler:           newHandler(store, api.New(store, manager, imgproxy.New(*demoMode), *noAuth)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // attachments
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		log.Printf("Mail listening on http://%s (database %s, %d accounts)", addr, path, len(accounts))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	cancel()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

// newHandler routes the API, the health check and the web app.
func newHandler(store *db.Store, a *api.API) http.Handler {
	app := noCache(http.FileServerFS(web.Files))
	mux := http.NewServeMux()
	mux.Handle("/api/", a.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := store.Ping(); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("/", app)
	return securityHeaders(mux)
}

// noCache makes browsers revalidate the web app on every load, so an upgrade
// never mixes old and new JavaScript modules.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets the headers of the app's pages. The frame showing a
// message replaces them with its own, stricter, policy.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-src 'self'; manifest-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: not a number", key, v)
	}
	return n
}

func envBool(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: expected true or false", key, v)
	}
	return b
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("invalid %s=%q: expected a duration such as 8760h", key, v)
	}
	return d
}
