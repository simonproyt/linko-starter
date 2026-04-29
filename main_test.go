package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"log/slog"
)

func Test_requestLogger(t *testing.T) {
	logBuffer := &bytes.Buffer{}

	logger := slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Time(slog.TimeKey, time.Date(2023, 10, 1, 12, 34, 57, 0, time.UTC))
			}
			if a.Key == "duration" {
				return slog.Duration("duration", 0)
			}
			return a
		},
	}))

	requestLoggerMiddleware := requestLogger(logger)
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	loggedHandler := requestLoggerMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "http://lin.ko/api/stats", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, req)

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.x:1234 duration=0s request_body_bytes=0 response_status=200 response_body_bytes=0` + "\n"
	const expectedStatusCode = http.StatusOK

	if got := logBuffer.String(); got != expectedLogString {
		t.Errorf("unexpected log output:\n got: %q\nwant: %q", got, expectedLogString)
	}
	if rr.Code != expectedStatusCode {
		t.Errorf("unexpected status code: got %d want %d", rr.Code, expectedStatusCode)
	}
}

func Test_requestLogger_includesRequestID(t *testing.T) {
	logBuffer := &bytes.Buffer{}

	logger := slog.New(slog.NewTextHandler(logBuffer, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Time(slog.TimeKey, time.Date(2023, 10, 1, 12, 34, 57, 0, time.UTC))
			}
			if a.Key == "duration" {
				return slog.Duration("duration", 0)
			}
			return a
		},
	}))

	requestLoggerMiddleware := requestLogger(logger)
	dummyHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	loggedHandler := requestLoggerMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "http://lin.ko/api/stats", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set("X-Request-ID", "req-123")
	rr := httptest.NewRecorder()
	loggedHandler.ServeHTTP(rr, req)

	const expectedLogString = `time=2023-10-01T12:34:57.000Z level=INFO msg="Served request" method=GET path=/api/stats client_ip=192.0.2.x:1234 duration=0s request_body_bytes=0 response_status=200 response_body_bytes=0 request_id=req-123` + "\n"

	if got := logBuffer.String(); got != expectedLogString {
		t.Errorf("unexpected log output:\n got: %q\nwant: %q", got, expectedLogString)
	}
	if rr.Code != http.StatusOK {
		t.Errorf("unexpected status code: got %d want %d", rr.Code, http.StatusOK)
	}
}
