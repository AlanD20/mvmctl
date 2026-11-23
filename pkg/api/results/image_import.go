package results

import "mvmctl/internal/lib/model"

// ImageImportResult binds import ownership to the actual persistence outcome.
type ImageImportResult struct {
	Image   *model.ImageItem `json:"image"`
	Created bool             `json:"created"`
}
