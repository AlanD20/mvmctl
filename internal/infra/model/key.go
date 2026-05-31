package model

// ── SSHKeyItem ──

// SSHKeyItem represents an SSH key record.
type SSHKeyItem struct {
	ID             string  `json:"id" db:"id"`
	Name           string  `json:"name" db:"name"`
	Fingerprint    string  `json:"fingerprint" db:"fingerprint"`
	Algorithm      string  `json:"algorithm" db:"algorithm"`
	Comment        string  `json:"comment" db:"comment"`
	PublicKeyPath  string  `json:"public_key_path" db:"public_key_path"`
	IsDefault      bool    `json:"is_default" db:"is_default"`
	IsPresent      bool    `json:"is_present" db:"is_present"`
	CreatedAt      string  `json:"created_at" db:"created_at"`
	UpdatedAt      string  `json:"updated_at" db:"updated_at"`
	PrivateKeyPath *string `json:"private_key_path" db:"private_key_path"`
}
