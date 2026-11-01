package kvhttp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestIDHeader carries the node's ID for each request. The client copies
// it into its errors, so a line in the CLI's log can be matched to the
// node's log line for the same request ("how to get more information").
const RequestIDHeader = "X-Request-Id"

// requestID gives every request an ID and returns it in RequestIDHeader.
func requestID(next http.Handler) http.Handler {
	return middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(RequestIDHeader, middleware.GetReqID(r.Context()))
		next.ServeHTTP(w, r)
	}))
}

// errSlot holds an error a handler couldn't act on, such as a failed write
// to a client that hung up.
//
// Each request runs on its own goroutine, and a handler is badly placed to
// decide what a failed write means. So, following Cox-Buday's rule ("send
// errors to another part of your program that has complete information"),
// the handler hands the error to the Logging middleware, which records it
// with the rest of the request.
type errSlot struct{ err error }

type errSlotKey struct{}

// reportErr passes err to the request's Logging middleware, if there is one.
// Only the first error is kept.
func reportErr(r *http.Request, err error) {
	if err == nil {
		return
	}
	if slot, ok := r.Context().Value(errSlotKey{}).(*errSlot); ok && slot.err == nil {
		slot.err = err
	}
}

// Logging writes one structured line per request: method, path, status,
// size, duration, request ID, and any error a handler reported. 5xx
// responses are logged at error level, 4xx and reported errors at warn, and
// everything else at info.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			slot := &errSlot{}
			r = r.WithContext(context.WithValue(r.Context(), errSlotKey{}, slot))
			start := time.Now()
			next.ServeHTTP(ww, r)

			level := slog.LevelInfo
			switch {
			case ww.Status() >= 500:
				level = slog.LevelError
			case ww.Status() >= 400 || slot.err != nil:
				level = slog.LevelWarn
			}
			attrs := []slog.Attr{
				slog.String("request_id", middleware.GetReqID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("duration", time.Since(start)),
			}
			if slot.err != nil {
				attrs = append(attrs, slog.String("error", slot.err.Error()))
			}
			logger.LogAttrs(r.Context(), level, "request", attrs...)
		})
	}
}

// Recover turns a panic in a handler (a bug, such as a failed assertion)
// into a 500 response that names the request ID, and logs the panic with
// its stack. The client sees a short message, and the log has the details.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler {
					panic(p) // net/http's own signal to abort; let it through
				}
				id := middleware.GetReqID(r.Context())
				logger.Error("panic serving request",
					slog.String("request_id", id),
					slog.String("panic", fmt.Sprint(p)),
					slog.String("stack", string(debug.Stack())),
				)
				http.Error(w, fmt.Sprintf("internal error (request id %s)", id), http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
