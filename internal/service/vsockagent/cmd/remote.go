package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"mvmctl/internal/service/vsockagent"
)

// remoteFrame is the wire format for response frames from the guest agent daemon.
type remoteFrame struct {
	Type   string `json:"type"`
	Status int    `json:"status,omitempty"`
	Data   string `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
}

// runRemoteSubcommand implements the "remote" subcommand: connect to the
// guest agent daemon's local Unix socket, send a RemoteVMRequest, and
// relay response frames to stdout/stderr. Returns the exit code.
func runRemoteSubcommand(socketPath string, args []string) int {
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: mvm-vsock-agent remote <destination> -- <command>\n")
		return 1
	}

	destination := args[0]
	// Find the command part after "--" or use remaining args.
	command := ""
	cmdArgs := args[1:]
	if len(cmdArgs) > 0 && cmdArgs[0] == "--" {
		command = strings.Join(cmdArgs[1:], " ")
	} else {
		command = strings.Join(cmdArgs, " ")
	}

	if command == "" {
		fmt.Fprintf(os.Stderr, "error: no command specified\n")
		return 1
	}

	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to connect to daemon socket %s: %v\n", socketPath, err)
		return 1
	}
	defer conn.Close()

	// Send the remote_vm request.
	req := vsockagent.RemoteVMRequest{
		Destination: destination,
		Command:     command,
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to send request: %v\n", err)
		return 1
	}

	// Relay response frames from the daemon to stdout/stderr.
	// The daemon forwards frames from the host's relay loop.
	var resp remoteFrame
	decoder := json.NewDecoder(conn)
	for {
		if err := decoder.Decode(&resp); err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "error: failed to parse response frame: %v\n", err)
			return 1
		}

		switch resp.Type {
		case "stdout":
			os.Stdout.WriteString(resp.Data)
		case "stderr":
			os.Stderr.WriteString(resp.Data)
		case "remote_vm":
			if resp.Error != "" {
				fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
				return resp.Status
			}
			return resp.Status
		}
	}

	return 0
}
