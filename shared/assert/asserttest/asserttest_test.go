package asserttest_test

import (
	"testing"

	"github.com/nickstrad/systems-learnings/shared/assert"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

func TestPanicsAcceptsAMatchingAssertion(t *testing.T) {
	asserttest.Panics(t, "x=7", func() { assert.That(false, "x=%d", 7) })
}

func TestFailPanicsOnlyWhenEnabled(t *testing.T) {
	asserttest.Panics(t, "boom", func() { assert.Fail("boom") })
}
