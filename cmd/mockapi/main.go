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
	)
	flag.Parse()

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

	srv, err := server.New(server.Options{
		Endpoints: registry,
		Logger:    log,
	})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", *addr, "ui", "http://localhost"+port(*addr)+server.UIPath)
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
