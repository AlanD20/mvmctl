package loopmount

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Config holds configuration for the loopmount provision wire protocol.
type Config struct {
	InputJSON string
	Umount    string
}

// Run executes the loopmount provision wire protocol with the given config.
// This is the canonical entry point called by the CLI layer.
func Run(ctx context.Context, cfg Config) error {
	// --umount shortcut
	if cfg.Umount != "" {
		if !CleanupMount(cfg.Umount) {
			return fmt.Errorf("failed to unmount: %s", cfg.Umount)
		}
		return nil
	}

	// Read JSON input from file or stdin.
	var raw []byte
	var err error
	if cfg.InputJSON != "" {
		raw, err = os.ReadFile(cfg.InputJSON)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		return fmt.Errorf("error reading input: %w", err)
	}

	// Parse and execute wire protocol.
	var input WireInput
	if err := json.Unmarshal(raw, &input); err != nil {
		fmt.Println(string(marshalWireError("Invalid JSON: "+err.Error(), "parse")))
		return nil
	}

	op, err := convertWireToOp(input)
	if err != nil {
		fmt.Println(string(marshalWireError(err.Error(), "parse")))
		return nil
	}

	provisioner := NewProvisioner("/tmp/mvm-provision")
	results, pErr := provisioner.Execute(ctx, []Op{op})
	if pErr != nil {
		fmt.Println(string(marshalWireError(pErr.Error(), "execute")))
		return nil
	}
	if len(results) == 0 {
		fmt.Println(string(marshalWireError("no result returned", "execute")))
		return nil
	}

	fmt.Println(string(marshalWireResult(results[0])))
	return nil
}
