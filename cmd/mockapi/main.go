// Command mockapi serves callback endpoints: URLs created on demand that
// capture the traffic third parties send them and answer per the user's spec.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/edspc/golang-mock-api-server/internal/auth"
	"github.com/edspc/golang-mock-api-server/internal/endpoint"
	"github.com/edspc/golang-mock-api-server/internal/persist"
	"github.com/edspc/golang-mock-api-server/internal/server"
	"github.com/edspc/golang-mock-api-server/internal/store"
)

// Storage is configured through the environment, and each half is independent:
// set neither and everything lives in memory, exactly as before.
const (
	envEndpointsDB = "MOCKAPI_ENDPOINTS_DB"
	envRequestsDB  = "MOCKAPI_REQUESTS_DB"
)

// Google sign-in is configured the same way: no keys, no authentication.
const (
	envClientID      = "GOOGLE_CLIENT_ID"
	envClientSecret  = "GOOGLE_CLIENT_SECRET"
	envBaseURL       = "MOCKAPI_BASE_URL"
	envAllowedEmails = "MOCKAPI_ALLOWED_EMAILS"
	envAllowedDomain = "MOCKAPI_ALLOWED_DOMAIN"
)

// authConfig reads the sign-in configuration. The base URL falls back to the
// listen address so a local trial needs one less variable; anything reachable
// from outside must set it, because Google matches the redirect URI exactly.
func authConfig(addr string) auth.Config {
	cfg := auth.Config{
		ClientID:      os.Getenv(envClientID),
		ClientSecret:  os.Getenv(envClientSecret),
		BaseURL:       os.Getenv(envBaseURL),
		AllowedDomain: os.Getenv(envAllowedDomain),
	}
	for _, email := range strings.Split(os.Getenv(envAllowedEmails), ",") {
		if email = strings.TrimSpace(email); email != "" {
			cfg.AllowedEmails = append(cfg.AllowedEmails, email)
		}
	}
	if cfg.BaseURL == "" && cfg.Configured() {
		cfg.BaseURL = "http://localhost" + port(addr)
	}
	return cfg
}

// version is stamped at build time with -ldflags "-X main.version=…". It stays
// "dev" for a plain `go build`, so an unstamped binary never claims to be a
// release.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mockapi:", err)
		os.Exit(1)
	}
}

// port turns a listen address into the ":port" suffix of a browsable URL.
func port(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":" + addr
}

// openStores wires whichever SQLite databases the environment names. It
// returns a function that closes what it opened.
func openStores(registry *endpoint.Registry, history int, log *slog.Logger) (func(), error) {
	endpointsPath, requestsPath := os.Getenv(envEndpointsDB), os.Getenv(envRequestsDB)
	if endpointsPath == "" && requestsPath == "" {
		log.Info("storage: in memory", "reason", envEndpointsDB+" and "+envRequestsDB+" are unset")
		return func() {}, nil
	}

	var closers []func()
	closeAll := func() {
		for _, c := range closers {
			c()
		}
	}

	var settings endpoint.Store
	if endpointsPath != "" {
		db, err := store.OpenEndpoints(endpointsPath)
		if err != nil {
			closeAll()
			return nil, err
		}
		closers = append(closers, func() { db.Close() })
		settings = persist.NewEndpoints(db)
		log.Info("storage: endpoints in sqlite", "path", endpointsPath)
	} else {
		log.Warn("storage: endpoints in memory, captured requests on disk — the request log will outlive the endpoints it belongs to",
			"hint", "set "+envEndpointsDB+" as well")
	}

	var historyFor func(string) endpoint.History
	if requestsPath != "" {
		db, err := store.OpenRequests(requestsPath, history)
		if err != nil {
			closeAll()
			return nil, err
		}
		closers = append(closers, func() { db.Close() })
		historyFor = persist.HistoryFor(db, log)
		log.Info("storage: captured requests in sqlite", "path", requestsPath)
	}

	registry.Persist(settings, historyFor, log)
	if err := registry.Restore(); err != nil {
		closeAll()
		return nil, fmt.Errorf("restore endpoints: %w", err)
	}
	log.Info("storage: restored", "endpoints", registry.Len())
	return closeAll, nil
}

func run() error {
	var (
		addr        = flag.String("addr", ":8080", "address to listen on")
		historySize = flag.Int("history", endpoint.DefaultHistory, "how many captured requests to keep per endpoint")
		quiet       = flag.Bool("quiet", false, "log warnings and errors only")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("mockapi", version)
		return nil
	}

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelWarn
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	registry := endpoint.NewRegistry(*historySize)
	closeStores, err := openStores(registry, *historySize, log)
	if err != nil {
		return err
	}
	defer closeStores()

	guard, err := auth.New(authConfig(*addr), log)
	if err != nil {
		return err
	}
	if guard.Enabled() {
		log.Info("sign-in: google oauth required for the control API",
			"redirect", strings.TrimSuffix(authConfig(*addr).BaseURL, "/")+server.AdminPrefix+"auth/callback")
		if !guard.Restricted() {
			// Not a warning: it is a supported setup, and the operator should
			// know which one they are running.
			log.Info("sign-in: open to any google account; each one sees only the endpoints it created",
				"restrict_with", envAllowedEmails+" or "+envAllowedDomain)
		}
		// Endpoints restored with no owner were created while sign-in was
		// off. They keep serving callbacks, but no account can see them —
		// say so once rather than let them look lost.
		if orphans := registry.Count(""); orphans > 0 {
			log.Warn("sign-in: endpoints created before sign-in was enabled are not visible to any account",
				"count", orphans, "note", "they keep answering callbacks")
		}
	} else {
		log.Info("sign-in: disabled", "reason", envClientID+" and "+envClientSecret+" are unset")
	}

	srv, err := server.New(server.Options{
		Endpoints: registry,
		Logger:    log,
		Auth:      guard,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		// Deliberately no WriteTimeout: /api/events is a stream that stays
		// open for as long as a console is watching, and a write deadline
		// would cut it off on a schedule.
	}
	// Which also means shutdown has to end those streams itself, or it would
	// wait out its timeout on a connection that is behaving exactly as
	// intended.
	httpSrv.RegisterOnShutdown(srv.CloseStreams)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "version", version, "addr", *addr, "ui", "http://localhost"+port(*addr)+server.UIPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return <-errCh
	}
}
