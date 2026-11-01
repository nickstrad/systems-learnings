package kvhttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/systems-learnings/jumphash/internal/assert"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
)

// Client talks to one node over HTTP. It satisfies cluster.Node.
type Client struct {
	base string
	hc   *http.Client
}

// NewClient returns a client for the node at base, e.g. "http://localhost:8081".
// Clients are cheap to create: they all share http.DefaultTransport, so
// connections are pooled across them.
func NewClient(base string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"),
		hc:   &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) Health(ctx context.Context) error {
	_, _, err := c.do(ctx, http.MethodGet, "/healthz", "", http.StatusOK)
	return err
}

// Get returns the value for key. ok is false if the node doesn't have it.
func (c *Client) Get(ctx context.Context, key string) (value string, ok bool, err error) {
	body, status, err := c.do(ctx, http.MethodGet, keyPath(key), "", http.StatusOK, http.StatusNotFound)
	if err != nil || status == http.StatusNotFound {
		return "", false, err
	}
	return string(body), true, nil
}

func (c *Client) Put(ctx context.Context, key, value string) error {
	_, _, err := c.do(ctx, http.MethodPut, keyPath(key), value, http.StatusNoContent)
	return err
}

// Delete removes key. Deleting a missing key is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	_, _, err := c.do(ctx, http.MethodDelete, keyPath(key), "", http.StatusNoContent, http.StatusNotFound)
	return err
}

// All returns every entry on the node.
//
//	post: err == nil ⇒ the map is non-nil (cluster.Node contract), even if
//	      the server sent JSON null
func (c *Client) All(ctx context.Context) (map[string]string, error) {
	body, _, err := c.do(ctx, http.MethodGet, "/kv", "", http.StatusOK)
	if err != nil {
		return nil, err
	}
	var out map[string]string
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, c.fail(err, http.MethodGet, "/kv", "node %s sent an unreadable key list", c.base)
	}
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

func keyPath(key string) string { return "/kv/" + url.PathEscape(key) }

// module is this package's name in well-formed errors.
const module = "kvhttp"

// do sends one request. It is the only place this client produces errors,
// so it is the module boundary: every error that leaves it is a
// well-formed kvhttp error carrying the URL, method, status, and the node's
// request ID (to find the node's log line).
//
//	pre:  at least one okStatus
//	post: err == nil ⇒ the returned status is one of okStatus
//	      err != nil ⇒ err is an *errs.Error from module "kvhttp"
func (c *Client) do(ctx context.Context, method, path, body string, okStatus ...int) ([]byte, int, error) {
	assert.That(len(okStatus) > 0, "kvhttp: do called without an expected status")
	wrap := func(err error, format string, args ...any) *errs.Error {
		return c.fail(err, method, path, format, args...)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, strings.NewReader(body))
	if err != nil {
		return nil, 0, wrap(err, "invalid node address %q", c.base)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, wrap(err, "request to node %s was cancelled", c.base)
		}
		return nil, 0, wrap(err, "cannot reach node %s (is it running?)", c.base)
	}
	defer resp.Body.Close()
	reqID := resp.Header.Get(RequestIDHeader)
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, wrap(err, "lost connection to node %s while reading its reply", c.base).With("request_id", reqID)
	}
	if slices.Contains(okStatus, resp.StatusCode) {
		return b, resp.StatusCode, nil
	}
	return nil, resp.StatusCode, wrap(nil, "node %s refused %s %s: %s", c.base, method, path, strings.TrimSpace(string(b))).
		With("status", resp.StatusCode).With("request_id", reqID)
}

// fail builds the client's well-formed error for one request.
func (c *Client) fail(err error, method, path, format string, args ...any) *errs.Error {
	return errs.Wrap(module, err, format, args...).With("method", method).With("url", c.base+path)
}

// NormalizeAddr turns "localhost:8081" or "http://localhost:8081/" into
// "http://localhost:8081", so one node always has one name.
//
// No assertion here on purpose: the input comes from the user (e.g. ""), so
// odd output is bad input, not a bug in this program.
func NormalizeAddr(addr string) string {
	addr = strings.TrimRight(strings.TrimSpace(addr), "/")
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}
	return addr
}
