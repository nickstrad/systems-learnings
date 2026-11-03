package kvhttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/nickstrad/systems-learnings/jumphash/internal/kv"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// MaxValueBytes is the largest value a PUT may carry (1 MiB). A bigger body
// gets 413 and nothing is stored; it is never silently cut short.
const MaxValueBytes = 1 << 20

// NewHandler is the full node: the routes from NewRouter wrapped in the
// middleware, in a fixed order. The request ID is outermost, so logging and
// panic recovery can both see it; logging wraps recovery, so a recovered
// panic is logged as a 500. Every response carries its ID in
// RequestIDHeader.
func NewHandler(s *kv.Store, logger *slog.Logger) http.Handler {
	return requestID(Logging(logger)(Recover(logger)(NewRouter(s))))
}

// NewRouter exposes a Store over HTTP, with no middleware:
//
//	GET    /healthz     200 "ok"
//	GET    /kv          200 JSON object of every entry
//	GET    /kv/{key}    200 value, or 404
//	PUT    /kv/{key}    204, body is the value; 400 unless the key and
//	                    value are valid UTF-8; 413 if the value is over
//	                    MaxValueBytes
//	DELETE /kv/{key}    204, or 404
//
// A node knows nothing about the cluster. Routing and data movement live in
// package cluster, which reaches nodes through Client.
func NewRouter(s *kv.Store) chi.Router {
	r := chi.NewRouter()

	// Write errors (e.g. the client hung up) can't be fixed here, so they
	// go to the request log via reportErr instead of being dropped.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.WriteString(w, "ok")
		reportErr(r, err)
	})

	r.Get("/kv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		reportErr(r, json.NewEncoder(w).Encode(s.Snapshot()))
	})

	r.Get("/kv/{key}", withKey(func(w http.ResponseWriter, r *http.Request, key string) {
		v, ok := s.Get(key)
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, err := io.WriteString(w, v)
		reportErr(r, err)
	}))

	r.Put("/kv/{key}", withKey(func(w http.ResponseWriter, r *http.Request, key string) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxValueBytes))
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("value too large: the limit is %d bytes; nothing was stored", MaxValueBytes),
				http.StatusRequestEntityTooLarge)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// GET /kv sends entries as JSON, which can't carry invalid UTF-8:
		// it would turn into U+FFFD, and a rebalance would copy the mangled
		// key and value. So refuse what we couldn't give back unchanged.
		if !utf8.ValidString(key) || !utf8.Valid(body) {
			http.Error(w, "key and value must be valid UTF-8", http.StatusBadRequest)
			return
		}
		s.Put(key, string(body))
		w.WriteHeader(http.StatusNoContent)
	}))

	r.Delete("/kv/{key}", withKey(func(w http.ResponseWriter, r *http.Request, key string) {
		if !s.Delete(key) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	return r
}

// withKey passes the decoded key to h. The route pattern {key} only matches
// a non-empty segment, and decoding never makes one empty, so the store
// never sees an empty key. That is asserted rather than checked.
func withKey(h func(w http.ResponseWriter, r *http.Request, key string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := keyParam(r)
		assert.That(key != "", "kvhttp: route matched an empty key")
		h(w, r, key)
	}
}

// keyParam returns the decoded {key}. chi matches on the escaped path when
// the URL has one (e.g. a key containing "/"), so decode only in that case.
func keyParam(r *http.Request) string {
	k := chi.URLParam(r, "key")
	if r.URL.RawPath == "" {
		return k
	}
	// net/http already rejected bad escapes when it parsed the URL, so this
	// cannot fail.
	d, err := url.PathUnescape(k)
	assert.That(err == nil, "kvhttp: bad escape %q in a parsed URL path", k)
	return d
}
