package main

import (
	"context"
	"fmt"
	"net/http"

	"log/slog"

	"go.opentelemetry.io/otel"

	pkgerr "github.com/pkg/errors"
	"golang.org/x/crypto/bcrypt"
)

type contextKey string

const UserContextKey contextKey = "user"

const LogContextKey contextKey = "log_context"

// LogContext holds request-scoped fields that downstream handlers can set
// for inclusion in the final request log.
type LogContext struct {
	Username string
	Error    error
}

// httpError stashes err into the request's LogContext (if present) and sends
// a safe response body for the client while preserving the full error for logs.
func httpError(ctx context.Context, w http.ResponseWriter, status int, err error) {
	if lc := ctx.Value(LogContextKey); lc != nil {
		if logCtx, ok := lc.(*LogContext); ok {
			if logCtx.Error == nil {
				logCtx.Error = err
			}
		}
	}

	body := err.Error()
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusInternalServerError:
		body = http.StatusText(status)
	}
	http.Error(w, body, status)
}

var allowedUsers = map[string]string{
	"frodo":   "$2a$10$B6O/n6teuCzpuh66jrUAdeaJ3WvXcxRkzpN0x7H.di9G9e/NGb9Me",
	"samwise": "$2a$10$EWZpvYhUJtJcEMmm/IBOsOGIcpxUnGIVMRiDlN/nxl1RRwWGkJtty",
	// frodo: "ofTheNineFingers"
	// samwise: "theStrong"
	"saruman": "invalidFormat",
}

func (s *server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, span := otel.Tracer("linko").Start(r.Context(), "auth.validate_password")
		defer span.End()
		r = r.WithContext(ctx)
		username, password, ok := r.BasicAuth()
		if !ok {
			httpError(r.Context(), w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
			return
		}
		stored, exists := allowedUsers[username]
		if !exists {
			httpError(r.Context(), w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
			return
		}
		ok, err := s.validatePassword(password, stored)
		if err != nil {
			// stash the original error so requestLogger can include stack trace
			if lc := r.Context().Value(LogContextKey); lc != nil {
				if logCtx, ok := lc.(*LogContext); ok {
					logCtx.Error = err
				}
			}
			s.logger.Error("error validating password", slog.String("user", username), slog.Any("error", err))
			httpError(r.Context(), w, http.StatusInternalServerError, fmt.Errorf("internal server error"))
			return
		}
		if !ok {
			httpError(r.Context(), w, http.StatusUnauthorized, fmt.Errorf("unauthorized"))
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), UserContextKey, username))
		// if a LogContext exists on the request, populate its Username so
		// requestLogger can include it in the final request log.
		if lc := r.Context().Value(LogContextKey); lc != nil {
			if logCtx, ok := lc.(*LogContext); ok {
				logCtx.Username = username
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) validatePassword(password, stored string) (bool, error) {
	err := bcrypt.CompareHashAndPassword([]byte(stored), []byte(password))
	if err == bcrypt.ErrMismatchedHashAndPassword {
		return false, nil
	}
	if err != nil {
		return false, pkgerr.WithStack(err)
	}
	return true, nil
}
