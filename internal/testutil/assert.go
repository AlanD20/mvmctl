package testutil

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// AssertDiff fails t with a diff between want and got if they are not deeply equal.
// The output uses the (-want +got) format standard in Go tests so failures
// show exactly what field differs rather than just a boolean "not equal".
func AssertDiff(t *testing.T, want, got any, msgAndArgs ...any) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		if len(msgAndArgs) > 0 {
			t.Errorf("(-want +got):\n%s\n%s", diff, msgAndArgs[0])
		} else {
			t.Errorf("(-want +got):\n%s", diff)
		}
	}
}
