package assert

import (
	"strings"
	"testing"
)

func TestThat(t *testing.T) {
	That(true, "never fails")

	defer func() {
		r := recover()
		if !Enabled {
			if r != nil {
				t.Fatalf("panicked with assertions disabled: %v", r)
			}
			return
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "assertion failed: x=7") {
			t.Fatalf("got panic %v", r)
		}
	}()
	That(false, "x=%d", 7)
}
