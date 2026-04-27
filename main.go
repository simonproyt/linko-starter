package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"log/slog"

	"boot.dev/linko/internal/store"
)

func requestLogger(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if logger != nil {
				logger.Info(fmt.Sprintf("Served request: %s %s", r.Method, r.URL.Path))
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
	debugHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})
	infoHandler := slog.NewTextHandler(f, &slog.HandlerOptions{Level: slog.LevelInfo})
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
	logger.Debug(fmt.Sprintf("Linko is shutting down"))
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string, logger *slog.Logger) int {
	st, err := store.New(logger, dataDir)
	if err != nil {
		logger.Error(fmt.Sprintf("failed to create store: %v", err))
		return 1
	}

	s := newServer(*st, httpPort, cancel, logger)
	logger.Debug(fmt.Sprintf("Linko is running on http://localhost:%d", httpPort))

	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Error(fmt.Sprintf("failed to shutdown server: %v", err))
		return 1
	}
	if serverErr != nil {
		logger.Error(fmt.Sprintf("server error: %v", serverErr))
		return 1
	}
	return 0
}
