// Package logx builds the structured loggers used by the binaries.
//
// Every line is JSON with a UTC timestamp. Cox-Buday's "Concurrency in Go"
// asks for errors to record when they happened in UTC, so logs from
// different machines can be lined up.
package logx

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
)

// New returns a JSON logger that writes to w at level and above, with
// timestamps in UTC.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				a.Value = slog.TimeValue(a.Value.Time().UTC())
			}
			return a
		},
	}))
}

// NewID returns a short random ID for a log entry, e.g. "3f9a1c0b". The
// user is shown the ID, and the log line with the full details carries it.
func NewID() string {
	b := make([]byte, 4)
	rand.Read(b) // never fails (crypto/rand docs, Go 1.24+)
	return hex.EncodeToString(b)
}
