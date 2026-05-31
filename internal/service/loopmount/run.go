// Package loopmount — "mvm run provision" entry point.
//
// Reads JSON from stdin or --input-json file, dispatches through the wire
// protocol handler, and writes JSON result to stdout.
package loopmount

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// RunProvision is the entry point for "mvm run provision".
// Parses args, reads JSON, executes, writes JSON result.
func RunProvision(args []string) {
	// Signal handling: SIGTERM/SIGINT → os.Exit(1)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		os.Exit(1)
	}()

	// Parse --input-json and --umount
	var inputJSONFile string
	var umountPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--input-json":
			if i+1 < len(args) {
				inputJSONFile = args[i+1]
				i++
			}
		case "--umount":
			if i+1 < len(args) {
				umountPath = args[i+1]
				i++
			}
		}
	}

	// --umount shortcut
	if umountPath != "" {
		if !CleanupMount(umountPath) {
			os.Exit(1)
		}
		return
	}

	// Read JSON input from file or stdin
	var raw []byte
	var err error
	if inputJSONFile != "" {
		raw, err = os.ReadFile(inputJSONFile)
	} else {
		raw, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
		os.Exit(1)
	}

	// Execute wire protocol
	resultJSON, err := ExecuteWireProtocol(context.Background(), raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, string(resultJSON))
		os.Exit(1)
	}
	fmt.Println(string(resultJSON))
}
