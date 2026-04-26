package main

import (
	"context"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

func requestLogger(logger *log.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if logger != nil {
				logger.Printf("Served request: %s %s", r.Method, r.URL.Path)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func initializeLogger() (*log.Logger, *os.File, error) {
	logFile := os.Getenv("LINKO_LOG_FILE")
	if logFile == "" {
		// only stderr
		return log.New(os.Stderr, "", log.LstdFlags), nil, nil
	}
	f, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	mw := io.MultiWriter(os.Stderr, f)
	return log.New(mw, "", log.LstdFlags), f, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	logger, f, err := initializeLogger()
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	if f != nil {
		defer f.Close()
	}

	status := run(ctx, cancel, *httpPort, *dataDir, logger)
	logger.Println("Linko is shutting down")
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string, logger *log.Logger) int {
	st, err := store.New(logger, dataDir)
	if err != nil {
		logger.Printf("failed to create store: %v", err)
		return 1
	}

	s := newServer(*st, httpPort, cancel, logger)
	logger.Printf("Linko is running on http://localhost:%d", httpPort)

	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		logger.Printf("failed to shutdown server: %v", err)
		return 1
	}
	if serverErr != nil {
		logger.Printf("server error: %v", serverErr)
		return 1
	}
	return 0
}
