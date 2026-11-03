// Package assert checks internal contracts: preconditions, postconditions,
// and invariants, in the design-by-contract sense.
//
// An assertion failure means there is a bug in this program, never bad
// input. Input from users, the network, or files is checked with ordinary
// errors. Assertions are for things that should be impossible, and they
// panic so the bug can't spread silently.
//
// Assertions are on by default. To build without them:
//
//	go build -tags noassert ./...
//
// With noassert, Enabled is a false constant, so the compiler removes code
// guarded by it. Wrap expensive checks (O(n) invariant walks) in
// `if assert.Enabled { ... }`, because the arguments to That are evaluated
// even when assertions are off.
package assert

import "fmt"

// That panics if cond is false.
//
// Go evaluates and boxes args before That runs, even when cond is true, so
// a call with arguments allocates on every pass. In hot paths, guard the
// call instead, which costs nothing when the check passes:
//
//	if !cond {
//		assert.Fail("x=%d", x)
//	}
func That(cond bool, format string, args ...any) {
	if Enabled && !cond {
		Fail(format, args...)
	}
}

// Fail panics with an assertion failure. It does nothing when assertions
// are compiled out. Call it on the failing branch of a hot-path check.
func Fail(format string, args ...any) {
	if Enabled {
		panic(fmt.Sprintf("assertion failed: "+format, args...))
	}
}
