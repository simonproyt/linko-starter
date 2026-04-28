package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log/slog"

	pkgerr "github.com/pkg/errors"

	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
)

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "err" || a.Key == "error" {
		if errVal, ok := a.Value.Any().(error); ok {
			// stackTracer extracts pkg/errors stack traces
			type stackTracer interface {
				error
				StackTrace() pkgerr.StackTrace
			}
			var st stackTracer
			if errors.As(errVal, &st) {
				attrs := []slog.Attr{
					{Key: "message", Value: slog.StringValue(st.Error())},
					{Key: "stack_trace", Value: slog.StringValue(fmt.Sprintf("%+v", st.StackTrace()))},
				}
				// include any attrs attached via linkoerr.WithAttrs
				attrs = append(attrs, linkoerr.Attrs(errVal)...)
				return slog.GroupAttrs("error", attrs...)
			}
			// Fallback to a grouped error with message + any attached attrs
			attrs := []slog.Attr{{Key: "message", Value: slog.StringValue(errVal.Error())}}
			attrs = append(attrs, linkoerr.Attrs(errVal)...)
			return slog.GroupAttrs("error", attrs...)
		}
	}
	return a
}

func requestLogger(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if logger != nil {
				logger.Info("Served request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("client_ip", r.RemoteAddr),
				)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func initializeLogger() (*slog.Logger, *os.File, error) {
	logFile := os.Getenv("LINKO_LOG_FILE")
	if logFile == "" {
		// only stderr: log DEBUG and above to stderr
		debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
		return slog.New(debugHandler), nil, nil
	}
	f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	// stderr: DEBUG and above; file: INFO and above
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: replaceAttr})
	infoHandler := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo, ReplaceAttr: replaceAttr})
	multi := slog.NewMultiHandler(debugHandler, infoHandler)
	logger := slog.New(multi)
	return logger, f, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	logger, f, err := initializeLogger()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	if f != nil {
		defer f.Close()
	}

	status := run(ctx, cancel, *httpPort, *dataDir, logger)
	logger.Debug("Linko is shutting down")
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string, logger *slog.Logger) int {
	st, err := store.New(logger, dataDir)
	if err != nil {
		logger.Error("failed to create store",
			slog.Any("error", err),
		)
		return 1
	}

	s := newServer(*st, httpPort, cancel, logger)
	logger.Debug("Linko is running", slog.Int("port", httpPort))

	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shutdown server",
			slog.Any("error", err),
		)
		return 1
	}
	if serverErr != nil {
		logger.Error("server error",
			slog.Any("error", serverErr),
		)
		return 1
	}
	return 0
}
