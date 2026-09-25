package env

import (
	"fmt"
	"os"
	"testing"
)

// TestMain prevents env lifecycle tests from persisting state in the operator's cache.
func TestMain(m *testing.M) {
	cacheDir, err := os.MkdirTemp("", "mvm-env-test-")
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("MVM_CACHE_DIR", cacheDir); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(cacheDir)
		os.Exit(1)
	}

	code := m.Run()
	if err := os.RemoveAll(cacheDir); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}
