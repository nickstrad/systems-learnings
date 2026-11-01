package kvhttp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"log/slog"

	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
	"github.com/nickstrad/systems-learnings/jumphash/internal/kv"
)

// Every error leaving the client is a well-formed kvhttp error.
func TestClientErrorsAreWellFormed(t *testing.T) {
	ctx := context.Background()

	t.Run("node down", func(t *testing.T) {
		srv := httptest.NewServer(NewRouter(kv.NewStore()))
		url := srv.URL
		srv.Close()
		err := NewClient(url).Health(ctx)
		e, ok := errs.Outermost(err)
		if !ok || e.Module != module || !strings.Contains(e.Message, "cannot reach node") {
			t.Fatalf("got %v", errs.Describe(err))
		}
		if e.Misc["url"] != url+"/healthz" {
			t.Fatalf("misc = %v", e.Misc)
		}
	})

	t.Run("node refuses, with its request ID", func(t *testing.T) {
		srv := httptest.NewServer(NewHandler(kv.NewStore(), slog.New(slog.DiscardHandler)))
		defer srv.Close()
		_, _, err := NewClient(srv.URL).do(ctx, http.MethodPut, "/kv/", "v", http.StatusNoContent)
		e, ok := errs.Outermost(err)
		if status, _ := e.Misc["status"].(int); !ok || e.Module != module || status < 400 || status >= 500 {
			t.Fatalf("got %v %v", errs.Describe(err), e.Misc)
		}
		if id, _ := e.Misc["request_id"].(string); id == "" {
			t.Fatal("error should carry the node's request ID")
		}
	})
}

// requireClientError fails unless err is a kvhttp error with a message
// containing mention and a url in its context.
func requireClientError(t *testing.T, err error, mention string) *errs.Error {
	t.Helper()
	e, ok := errs.Outermost(err)
	if !ok || e.Module != module || !strings.Contains(e.Message, mention) || e.Misc["url"] == nil {
		t.Fatalf("want a kvhttp error mentioning %q, got %s", mention, errs.Describe(err))
	}
	return e
}

// Replies the client can't use are still reported as well-formed errors.
func TestClientReportsBadReplies(t *testing.T) {
	ctx := context.Background()
	serve := func(t *testing.T, h http.HandlerFunc) *Client {
		srv := httptest.NewServer(h)
		t.Cleanup(srv.Close)
		return NewClient(srv.URL)
	}

	t.Run("bad JSON from All", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{not json") })
		_, err := c.All(ctx)
		requireClientError(t, err, "unreadable key list")
	})

	t.Run("JSON null from All is an empty map", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "null") })
		if m, err := c.All(ctx); err != nil || m == nil || len(m) != 0 {
			t.Fatalf("got (%v, %v)", m, err)
		}
	})

	t.Run("connection dies mid-body", func(t *testing.T) {
		c := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "100")
			io.WriteString(w, "partial")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // closes the connection
		})
		_, _, err := c.Get(ctx, "k")
		requireClientError(t, err, "lost connection")
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("lost the cause: %s", errs.Describe(err))
		}
	})

	t.Run("All on an unreachable node", func(t *testing.T) {
		c := serve(t, func(http.ResponseWriter, *http.Request) {})
		c.base = "http://127.0.0.1:1"
		_, err := c.All(ctx)
		requireClientError(t, err, "cannot reach node")
	})

	t.Run("invalid base URL", func(t *testing.T) {
		err := NewClient("http://bad host").Health(ctx)
		requireClientError(t, err, "invalid node address")
	})

	t.Run("cancelled context", func(t *testing.T) {
		c := serve(t, func(http.ResponseWriter, *http.Request) {})
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		err := c.Health(cancelled)
		requireClientError(t, err, "was cancelled")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("lost the cause: %s", errs.Describe(err))
		}
	})
}

func TestNormalizeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"localhost:8081":            "http://localhost:8081",
		" localhost:8081/ ":         "http://localhost:8081",
		"http://localhost:8081/":    "http://localhost:8081",
		"https://example.com:9000/": "https://example.com:9000",
	} {
		if got := NormalizeAddr(in); got != want {
			t.Errorf("NormalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServerLogsRequestsWithIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	srv := httptest.NewServer(NewHandler(kv.NewStore(), logger))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/kv/missing")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := resp.Header.Get(RequestIDHeader)
	if id == "" {
		t.Fatal("response has no request ID header")
	}
	if !strings.Contains(buf.String(), id) || !strings.Contains(buf.String(), `"status":404`) {
		t.Fatalf("log line missing request ID or status: %s", buf.String())
	}
}

func TestRecoverTurnsPanicInto500(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := requestID(Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("assertion failed: boom")
	})))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	id := rec.Header().Get(RequestIDHeader)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), id) {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(buf.String(), "boom") || !strings.Contains(buf.String(), "stack") {
		t.Fatalf("panic not logged with stack: %s", buf.String())
	}
}

// failingWriter is a client that hung up: every write fails.
type failingWriter struct{ h http.Header }

func (f *failingWriter) Header() http.Header       { return f.h }
func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }
func (f *failingWriter) WriteHeader(int)           {}

// A handler can't fix a failed write, so it hands the error to the logging
// layer instead of dropping it (Cox-Buday, chapter 4).
func TestHandlerWriteErrorReachesTheLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	store := kv.NewStore()
	store.Put("k", "v")
	h := Logging(logger)(NewRouter(store))

	h.ServeHTTP(&failingWriter{h: http.Header{}}, httptest.NewRequest(http.MethodGet, "/kv", nil))
	if !strings.Contains(buf.String(), `"error":"broken pipe"`) || !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("write error not logged: %s", buf.String())
	}
}

// A 5xx response is logged at error level.
func TestServerErrorsAreLoggedAtErrorLevel(t *testing.T) {
	var buf bytes.Buffer
	h := Logging(slog.New(slog.NewJSONHandler(&buf, nil)))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(buf.String(), `"level":"ERROR"`) {
		t.Fatalf("got %s", buf.String())
	}
}

// net/http's abort signal is not a bug: Recover passes it on instead of
// turning it into a 500.
func TestRecoverPassesOnAbortHandler(t *testing.T) {
	h := Recover(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Fatalf("recovered %v", p)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

// brokenBody fails when read, like a client that disconnects mid-upload.
type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func TestPutWithUnreadableBodyIsBadRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	NewRouter(kv.NewStore()).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/kv/k", brokenBody{}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}
