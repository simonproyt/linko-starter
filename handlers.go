package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"log/slog"

	"boot.dev/linko/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const shortURLLen = len("http://localhost:8080/") + 6

var (
	redirectsMu sync.Mutex
	redirects   []string
)

//go:embed index.html
var indexPage string

func (s *server) handlerIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	io.WriteString(w, indexPage)
}

func (s *server) handlerLogin(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *server) handlerShortenLink(w http.ResponseWriter, r *http.Request) {
	user, ok := r.Context().Value(UserContextKey).(string)
	if !ok || user == "" {
		httpError(r.Context(), w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
		return
	}
	longURL := r.FormValue("url")
	if longURL == "" {
		httpError(r.Context(), w, http.StatusBadRequest, fmt.Errorf("missing url parameter"))
		return
	}
	u, err := url.Parse(longURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		httpError(r.Context(), w, http.StatusBadRequest, fmt.Errorf("invalid URL: must include scheme (http/https) and host"))
		return
	}
	// Intentionally logging only the final success event to avoid redundant log entries
	if err := checkDestination(longURL); err != nil {
		if lc := r.Context().Value(LogContextKey); lc != nil {
			if logCtx, ok := lc.(*LogContext); ok {
				logCtx.Error = err
			}
		}
		httpError(r.Context(), w, http.StatusBadRequest, fmt.Errorf("invalid target URL: %v", err))
		return
	}
	shortCode, err := s.store.Create(r.Context(), longURL)
	if err != nil {
		if lc := r.Context().Value(LogContextKey); lc != nil {
			if logCtx, ok := lc.(*LogContext); ok {
				logCtx.Error = err
			}
		}
		httpError(r.Context(), w, http.StatusInternalServerError, fmt.Errorf("failed to shorten URL"))
		return
	}
	s.logger.Info("Successfully generated short code",
		slog.String("short_code", shortCode),
		slog.String("long_url", longURL),
		slog.String("url", longURL),
		slog.String("user", user),
	)
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusCreated)
	io.WriteString(w, shortCode)
}

func (s *server) handlerRedirect(w http.ResponseWriter, r *http.Request) {
	shortCode := strings.TrimPrefix(r.URL.Path, "/")
	longURL, err := s.store.Lookup(r.Context(), shortCode)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(r.Context(), w, http.StatusNotFound, fmt.Errorf("not found"))
		} else {
			s.logger.Error("failed to lookup URL",
				slog.Any("error", err),
			)
			if lc := r.Context().Value(LogContextKey); lc != nil {
				if logCtx, ok := lc.(*LogContext); ok {
					logCtx.Error = err
				}
			}
			httpError(r.Context(), w, http.StatusInternalServerError, fmt.Errorf("internal server error"))
		}
		return
	}
	_, _ = bcrypt.GenerateFromPassword([]byte(longURL), bcrypt.DefaultCost)
	if err := checkDestination(longURL); err != nil {
		if lc := r.Context().Value(LogContextKey); lc != nil {
			if logCtx, ok := lc.(*LogContext); ok {
				logCtx.Error = err
			}
		}
		httpError(r.Context(), w, http.StatusBadGateway, fmt.Errorf("destination unavailable"))
		return
	}

	redirectsMu.Lock()
	redirects = append(redirects, strings.Repeat(longURL, 1024))
	redirectsMu.Unlock()

	http.Redirect(w, r, longURL, http.StatusFound)
}

// we need to make this use the requstLogger middleware to log the request when we list the URLs
func (s *server) handlerListURLs(w http.ResponseWriter, r *http.Request) {
	codes, err := s.store.List(r.Context())
	if err != nil {
		s.logger.Error("failed to list URLs",
			slog.Any("error", err),
		)
		if lc := r.Context().Value(LogContextKey); lc != nil {
			if logCtx, ok := lc.(*LogContext); ok {
				logCtx.Error = err
			}
		}
		httpError(r.Context(), w, http.StatusInternalServerError, fmt.Errorf("failed to list URLs"))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(codes)
}

func (s *server) handlerStats(w http.ResponseWriter, _ *http.Request) {
	redirectsMu.Lock()
	snapshot := redirects
	redirectsMu.Unlock()

	var bytesSaved int
	for _, u := range snapshot {
		bytesSaved += len(u) - shortURLLen
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{
		"redirects":   len(snapshot),
		"bytes_saved": bytesSaved,
	})
}
