package errs

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

var errSentinel = errors.New("sentinel")

func TestWrapCarriesWhatWhenWhere(t *testing.T) {
	before := time.Now().UTC()
	e := Wrap("low", errSentinel, "cannot do %s", "x").With("path", "/tmp/a")

	if e.Error() != "cannot do x" {
		t.Fatalf("Error() = %q, want only the message", e.Error())
	}
	if e.Time.Location() != time.UTC || e.Time.Before(before) {
		t.Fatalf("time %v is not a UTC time from this call", e.Time)
	}
	if !strings.Contains(e.Stack, "TestWrapCarriesWhatWhenWhere") {
		t.Fatal("stack does not show where the error was wrapped")
	}
	if e.Misc["path"] != "/tmp/a" {
		t.Fatalf("misc = %v", e.Misc)
	}
}

func TestChainAndUnwrap(t *testing.T) {
	raw := fmt.Errorf("dial: %w", errSentinel)
	low := Wrap("low", raw, "cannot reach node").With("url", "http://x")
	top := Wrap("top", low, "cannot add node")

	if !errors.Is(top, errSentinel) {
		t.Fatal("errors.Is must see through the wrapping")
	}
	var got *Error
	if !errors.As(top, &got) || got.Module != "top" {
		t.Fatal("errors.As must find the outermost *Error")
	}

	chain := Chain(top)
	if len(chain) != 4 {
		t.Fatalf("chain has %d layers, want 4: %+v", len(chain), chain)
	}
	if chain[0].Module != "top" || chain[1].Module != "low" || chain[2].Module != "" {
		t.Fatalf("unexpected chain: %+v", chain)
	}
	if chain[1].Misc["url"] != "http://x" || chain[0].Misc != nil {
		t.Fatalf("misc should be listed only on layers that have it: %+v", chain)
	}
	if want := "top: cannot add node <- low: cannot reach node <- dial: sentinel <- sentinel"; Describe(top) != want {
		t.Fatalf("Describe = %q", Describe(top))
	}
	if DeepestStack(top) != low.Stack {
		t.Fatal("DeepestStack should return the innermost wrap's stack")
	}
}

func TestOutermostDoesNotSearchTheChain(t *testing.T) {
	inner := Wrap("low", nil, "x")
	if _, ok := Outermost(fmt.Errorf("context: %w", inner)); ok {
		t.Fatal("an error wrapped by fmt.Errorf is not well-formed at this level")
	}
	if e, ok := Outermost(inner); !ok || e != inner {
		t.Fatal("Outermost should accept an *Error")
	}
}

func TestStackIsCapturedOnlyWhereTheErrorStarts(t *testing.T) {
	inner := Wrap("low", errors.New("disk full"), "cannot write")
	outer := Wrap("high", inner, "cannot save")
	if inner.Stack == "" {
		t.Error("the first *Error in the chain must capture a stack")
	}
	if outer.Stack != "" {
		t.Error("wrapping an *Error must not capture a second stack")
	}
	if DeepestStack(outer) != inner.Stack {
		t.Error("DeepestStack must return the origin's stack")
	}
}
