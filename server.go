package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"

	"log/slog"

	"boot.dev/linko/internal/store"
)

type server struct {
	httpServer *http.Server
	store      store.Store
	cancel     context.CancelFunc
	logger     *slog.Logger
}

func newServer(store store.Store, port int, cancel context.CancelFunc, logger *slog.Logger) *server {
	mux := http.NewServeMux()

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}

	s := &server{
		httpServer: srv,
		store:      store,
		cancel:     cancel,
		logger:     logger,
	}

	wrapHandler := func(h http.Handler) http.Handler {
		return requestID(requestLogger(s.logger)(h))
	}

	mux.Handle("/api/login", wrapHandler(s.authMiddleware(http.HandlerFunc(s.handlerLogin))))
	mux.Handle("/api/shorten", wrapHandler(s.authMiddleware(http.HandlerFunc(s.handlerShortenLink))))
	mux.Handle("/api/stats", wrapHandler(s.authMiddleware(http.HandlerFunc(s.handlerStats))))
	mux.Handle("/api/urls", wrapHandler(s.authMiddleware(http.HandlerFunc(s.handlerListURLs))))
	mux.Handle("/admin/shutdown", wrapHandler(http.HandlerFunc(s.handlerShutdown)))

	// Root: exact "/" serves index; anything else at root level is treated as a short code redirect.
	mux.Handle("/", wrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			s.handlerIndex(w, r)
			return
		}
		s.handlerRedirect(w, r)
	})))

	return s
}

func (s *server) start() error {
	ln, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	if err := s.httpServer.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *server) shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *server) handlerShutdown(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ENV") == "production" {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusOK)
	go s.cancel()
}
