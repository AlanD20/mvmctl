package results

import "mvmctl/internal/lib/model"

// ImagePullResult binds pull ownership to the actual persistence outcome.
type ImagePullResult struct {
	Image   *model.ImageItem `json:"image"`
	Created bool             `json:"created"`
}
