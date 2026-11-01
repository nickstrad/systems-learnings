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

// That panics if cond is false. In hot paths, pass no args, so that no
// argument slice is allocated.
func That(cond bool, format string, args ...any) {
	if Enabled && !cond {
		panic(fmt.Sprintf("assertion failed: "+format, args...))
	}
}
