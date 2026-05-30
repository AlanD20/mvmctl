package inputs

// KeyImportInput holds options for importing an existing public key.
// Matches the API-level input used by KeyOperation.add() in Python.
type KeyImportInput struct {
	Name          string
	PubKeyPath    string
	PubKeyContent string
	Overwrite     bool
	SetDefault    bool
}
