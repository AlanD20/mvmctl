package responses

// KeyInfo groups key metadata in an inspect response.
type KeyInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	Algorithm   string `json:"algorithm"`
	Comment     string `json:"comment"`
	IsDefault   bool   `json:"is_default"`
	IsPresent   bool   `json:"is_present"`
}

// KeyFilesInfo groups key file paths in an inspect response.
type KeyFilesInfo struct {
	PublicKeyPath  string  `json:"public_key_path"`
	PrivateKeyPath *string `json:"private_key_path"`
}

// KeyTimestampsInfo groups key timestamps in an inspect response.
type KeyTimestampsInfo struct {
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// KeyInspect is the structured response for key inspection.
type KeyInspect struct {
	Key        KeyInfo           `json:"key"`
	Files      KeyFilesInfo      `json:"files"`
	Timestamps KeyTimestampsInfo `json:"timestamps"`
}
