package model

// ── SSHKeyItem ──

// SSHKeyItem represents an SSH key record.
type SSHKeyItem struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Fingerprint    string  `json:"fingerprint"`
	Algorithm      string  `json:"algorithm"`
	Comment        string  `json:"comment"`
	PublicKeyPath  string  `json:"public_key_path"`
	IsDefault      bool    `json:"is_default"`
	IsPresent      bool    `json:"is_present"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	PrivateKeyPath *string `json:"private_key_path"`
}
