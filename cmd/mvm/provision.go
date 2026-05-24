package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"mvmctl/internal/service/loopmount"
)

// runProvision — entry point for the _provision hidden subcommand.
// Parses args, reads JSON from stdin/file, delegates to the service's
// wire protocol handler, and writes JSON result to stdout.
func runProvision(args []string) {
	// Set up signal handlers matching Python: SIGTERM/SIGINT → os.Exit(1)
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		os.Exit(1)
	}()

	// Parse args using --input-json and --umount flags matching Python's argparse.
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

	// --umount shortcut (unmount + rmdir, no JSON input)
	if umountPath != "" {
		if !loopmount.CleanupMount(umountPath) {
			os.Exit(1)
		}
		return
	}

	// Read input: from file (--input-json) or stdin
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

	// Execute wire protocol — the service handles JSON parse, conversion, execution
	resultJSON, err := loopmount.ExecuteWireProtocol(raw)
	if err != nil {
		// Result JSON already contains Status "error" — write it and exit
		fmt.Println(string(resultJSON))
		os.Exit(1)
	}

	fmt.Println(string(resultJSON))
}
