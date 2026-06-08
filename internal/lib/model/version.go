package model

// ── VersionInfo ──

// VersionInfo represents a published version of a resource from an upstream provider.
type VersionInfo struct {
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	SHA256URL   string `json:"sha256_url,omitempty"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Format      string `json:"format"`
	Name        string `json:"name,omitempty"`
	IsPresent   bool   `json:"is_present,omitempty"`
}
