// Package errs is the well-formed error type, following the error
// propagation advice in Cox-Buday's "Concurrency in Go" (chapter 5).
//
// Every error a user might see should carry:
//
//  1. what happened             (Inner: the cause)
//  2. when and where            (Time in UTC, Stack, Module, Misc)
//  3. a friendly message        (Message, which is what Error returns)
//  4. how to get more info      (a log ID, added by the top level)
//
// Each module wraps every error that leaves its public functions:
//
//	return errs.Wrap("kvhttp", err, "cannot reach node %s", addr)
//
// An error that reaches the top level without being wrapped by the
// top-level module is a bug, and is reported as one.
//
// Adapted to modern Go: Unwrap makes errors.Is and errors.As see through
// the wrapping. A Module field stands in for the book's one-type-per-module
// (LowLevelErr, IntermediateErr).
package errs

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"
)

// Error is a well-formed error.
type Error struct {
	Module  string         // the module boundary that wrapped it, e.g. "cluster"
	Message string         // one line, safe to show to a user
	Inner   error          // the cause; nil if this module created the error
	Time    time.Time      // when it was wrapped, in UTC
	Stack   string         // stack trace where the error started; empty if Inner already has one
	Misc    map[string]any // extra context: url, path, request ID, ...
}

// Wrap returns a well-formed error from module. err may be nil, when the
// module itself detected the problem. The stack is captured only where the
// error starts: if err already contains an *Error, its stack is the useful
// one (see DeepestStack).
//
//	pre:  module != "" and the message is not empty
func Wrap(module string, err error, format string, args ...any) *Error {
	e := &Error{
		Module:  module,
		Message: fmt.Sprintf(format, args...),
		Inner:   err,
		Time:    time.Now().UTC(),
		Misc:    map[string]any{},
	}
	if _, wrapped := errors.AsType[*Error](err); !wrapped {
		e.Stack = string(debug.Stack())
	}
	return e
}

// With adds a piece of context to Misc and returns e, for chaining.
func (e *Error) With(key string, value any) *Error {
	e.Misc[key] = value
	return e
}

// Error returns only the friendly message, as in the book. The full chain
// is in Chain, and it belongs in logs.
func (e *Error) Error() string { return e.Message }

func (e *Error) Unwrap() error { return e.Inner }

// Outermost returns err as an *Error only if the outermost error is one,
// without searching the chain. This is the book's check: an error that
// skipped a module's wrapping is malformed.
func Outermost(err error) (*Error, bool) {
	e, ok := err.(*Error)
	return e, ok
}

// Layer is one step of an error chain, for logging.
type Layer struct {
	Module  string         `json:"module,omitempty"`
	Message string         `json:"message"`
	Time    string         `json:"time,omitempty"`
	Misc    map[string]any `json:"misc,omitempty"`
}

// Chain lists every layer of err from outermost to innermost. Raw errors
// (not *Error) appear with only a message, which is how a log shows where
// wrapping stopped.
func Chain(err error) []Layer {
	var out []Layer
	for err != nil {
		if e, ok := err.(*Error); ok {
			l := Layer{Module: e.Module, Message: e.Message, Time: e.Time.Format(time.RFC3339Nano)}
			if len(e.Misc) > 0 {
				l.Misc = e.Misc
			}
			out = append(out, l)
		} else {
			out = append(out, Layer{Message: err.Error()})
		}
		err = errors.Unwrap(err)
	}
	return out
}

// DeepestStack returns the stack of the innermost *Error, which is closest
// to where things went wrong.
func DeepestStack(err error) string {
	stack := ""
	for err != nil {
		if e, ok := err.(*Error); ok && e.Stack != "" {
			stack = e.Stack
		}
		err = errors.Unwrap(err)
	}
	return stack
}

// Describe is a one-line dump of the whole chain, for test failures and
// debugging.
func Describe(err error) string {
	var parts []string
	for _, l := range Chain(err) {
		if l.Module != "" {
			parts = append(parts, l.Module+": "+l.Message)
		} else {
			parts = append(parts, l.Message)
		}
	}
	return strings.Join(parts, " <- ")
}
