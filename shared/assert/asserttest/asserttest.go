// Package asserttest helps tests show that an assertion can fail: a
// contract check that has never tripped proves nothing (docs/testing.md).
package asserttest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Panics runs fn and fails the test unless it panics with an assertion
// failure whose message contains want. With assertions compiled out
// (-tags noassert) it skips the test instead.
func Panics(t *testing.T, want string, fn func()) {
	t.Helper()
	if !assert.Enabled {
		t.Skip("assertions are compiled out")
	}
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("no panic; want an assertion failure containing %q", want)
		}
		msg := fmt.Sprint(r)
		if !strings.HasPrefix(msg, "assertion failed: ") || !strings.Contains(msg, want) {
			t.Fatalf("panic %q; want an assertion failure containing %q", msg, want)
		}
	}()
	fn()
}
