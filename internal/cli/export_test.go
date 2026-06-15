// Package cli — test-only exports for external test package pattern.
package cli

// Exported for testing in package cli_test.
var (
	FormatChange              = formatChange
	AbortIfVMsRunning         = abortIfVMsRunning
	ResourceDisplayName       = resourceDisplayName
	ResourceDisplayNamePlural = resourceDisplayNamePlural
)
