package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"log/slog"

	pkgerr "github.com/pkg/errors"

	"boot.dev/linko/internal/build"
	"boot.dev/linko/internal/linkoerr"
	"boot.dev/linko/internal/store"
)

// spyReadCloser wraps an io.ReadCloser and counts bytes read.
type spyReadCloser struct {
	io.ReadCloser
	bytesRead int
}

func (s *spyReadCloser) Read(p []byte) (int, error) {
	n, err := s.ReadCloser.Read(p)
	s.bytesRead += n
	return n, err
}

// spyResponseWriter wraps http.ResponseWriter and captures status and bytes written.
type spyResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *spyResponseWriter) Write(p []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytesWritten += n
	return n, err
}

func (w *spyResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

func replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if a.Key == "err" || a.Key == "error" {
		if errVal, ok := a.Value.Any().(error); ok {
			// helper to build attrs for a single error
			type stackTracer interface {
				error
				StackTrace() pkgerr.StackTrace
			}

			errorAttrs := func(err error) []slog.Attr {
				attrs := []slog.Attr{{Key: "message", Value: slog.StringValue(err.Error())}}
				// attach any linkoerr attributes (outermost first)
				attrs = append(attrs, linkoerr.Attrs(err)...)
				var st stackTracer
				if errors.As(err, &st) {
					attrs = append(attrs, slog.Attr{Key: "stack_trace", Value: slog.StringValue(fmt.Sprintf("%+v", st.StackTrace()))})
				}
				return attrs
			}

			// detect joined/multi errors
			type multiError interface {
				error
				Unwrap() []error
			}
			var me multiError
			if errors.As(errVal, &me) {
				var grouped []slog.Attr
				for i, sub := range me.Unwrap() {
					grouped = append(grouped, slog.GroupAttrs(fmt.Sprintf("error_%d", i+1), errorAttrs(sub)...))
				}
				return slog.GroupAttrs("errors", grouped...)
			}

			// single error -> grouped error object
			return slog.GroupAttrs("error", errorAttrs(errVal)...)
		}
	}
	return a
}

func requestLogger(logger *slog.Logger) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// create & attach a request-scoped LogContext for downstream handlers
			logCtx := &LogContext{}
			r = r.WithContext(context.WithValue(r.Context(), LogContextKey, logCtx))

			// ensure non-nil body to simplify spying
			if r.Body == nil {
				r.Body = io.NopCloser(strings.NewReader(""))
			}
			spyReader := &spyReadCloser{ReadCloser: r.Body}
			r.Body = spyReader

			spyWriter := &spyResponseWriter{ResponseWriter: w}

			next.ServeHTTP(spyWriter, r)

			// default status if none was written
			if spyWriter.statusCode == 0 {
				spyWriter.statusCode = http.StatusOK
			}

			if logger != nil {
				// build attrs so we can conditionally include username
				attrs := []slog.Attr{
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("client_ip", r.RemoteAddr),
					slog.Duration("duration", time.Since(start)),
					slog.Int("request_body_bytes", spyReader.bytesRead),
					slog.Int("response_status", spyWriter.statusCode),
					slog.Int("response_body_bytes", spyWriter.bytesWritten),
				}
				if logCtx != nil && logCtx.Username != "" {
					attrs = append(attrs, slog.String("user", logCtx.Username))
				}
				if logCtx != nil && logCtx.Error != nil {
					attrs = append(attrs, slog.Any("error", logCtx.Error))
				}
				anyArgs := make([]any, 0, len(attrs))
				for _, a := range attrs {
					anyArgs = append(anyArgs, a)
				}
				logger.Info("Served request", anyArgs...)
			}
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

	// attach build and instance metadata to the logger
	hostname, _ := os.Hostname()
	logger = logger.With(
		slog.String("git_sha", build.GitSHA),
		slog.String("build_time", build.BuildTime),
		slog.String("env", os.Getenv("ENV")),
		slog.String("hostname", hostname),
	)

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
