package model

// ── VMInfo ──

// VMInfo holds the resolved VM identifier fields for log operations.
// Matches Python's LogController.vm property.
type VMInfo struct {
	Hash string
	Dir  string
	Name string
}
