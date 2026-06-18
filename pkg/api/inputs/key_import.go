package inputs

// KeyImportInput holds options for importing an existing public key.
type KeyImportInput struct {
	Name          string
	PubKeyPath    string
	PubKeyContent string
	Overwrite     bool
	SetDefault    bool
}
