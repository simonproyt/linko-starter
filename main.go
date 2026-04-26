package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"boot.dev/linko/internal/store"
)

func requestLogger(accessLogger *log.Logger, standardLogger *log.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if accessLogger != nil {
				accessLogger.Printf("Served request: %s %s", r.Method, r.URL.Path)
			}
			if standardLogger != nil {
				standardLogger.Printf("Served request: %s %s", r.Method, r.URL.Path)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	httpPort := flag.Int("port", 8899, "port to listen on")
	dataDir := flag.String("data", "./data", "directory to store data")
	flag.Parse()

	// create two non-global loggers
	accessFile, err := os.OpenFile("linko.access.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		accessFile = os.Stderr
	}
	accessLogger := log.New(accessFile, "INFO: ", log.LstdFlags)
	standardLogger := log.New(os.Stderr, "DEBUG: ", log.LstdFlags)

	status := run(ctx, cancel, *httpPort, *dataDir, standardLogger, accessLogger)
	standardLogger.Println("Linko is shutting down")
	os.Exit(status)
}

func run(ctx context.Context, cancel context.CancelFunc, httpPort int, dataDir string, standardLogger *log.Logger, accessLogger *log.Logger) int {
	st, err := store.New(standardLogger, dataDir)
	if err != nil {
		standardLogger.Printf("failed to create store: %v", err)
		return 1
	}

	s := newServer(*st, httpPort, cancel, accessLogger, standardLogger)
	accessLogger.Printf("Linko is running on http://localhost:%d", httpPort)

	var serverErr error
	go func() {
		serverErr = s.start()
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.shutdown(shutdownCtx); err != nil {
		standardLogger.Printf("failed to shutdown server: %v", err)
		return 1
	}
	if serverErr != nil {
		standardLogger.Printf("server error: %v", serverErr)
		return 1
	}
	return 0
}
