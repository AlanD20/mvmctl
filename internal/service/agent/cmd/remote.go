package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"mvmctl/internal/lib/vmexecwire"
)

// runRemoteSubcommand sends one process-bound source request to the guest agent daemon.
func runRemoteSubcommand(socketPath string, args []string) int {
	request, err := parseRemoteRequest(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage: mvm-agent remote --user <non-root> <destination> -- <command>")
		return 1
	}
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", socketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to connect to source broker")
		return 1
	}
	defer conn.Close()
	return runRemoteExchange(conn, request)
}

func runRemoteExchange(conn net.Conn, request vmexecwire.SourceRequest) int {
	if err := vmexecwire.EncodeSourceRequest(context.Background(), conn, request); err != nil {
		fmt.Fprintln(os.Stderr, "error: failed to send source request")
		return 1
	}
	reader, err := vmexecwire.NewResponseReader(conn, request.RequestID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: invalid source response")
		return 1
	}

	exitCode := -1
	terminalError := false
	for {
		frame, readErr := reader.Read(context.Background())
		if errors.Is(readErr, io.EOF) {
			if exitCode >= 0 {
				return exitCode
			}
			if terminalError {
				fmt.Fprintln(os.Stderr, "error: source execution failed")
				return 1
			}
			fmt.Fprintln(os.Stderr, "error: invalid source response")
			return 1
		}
		if readErr != nil {
			fmt.Fprintln(os.Stderr, "error: invalid source response")
			return 1
		}
		switch value := frame.(type) {
		case vmexecwire.StdoutFrame:
			if _, err := os.Stdout.Write(value.Data); err != nil {
				fmt.Fprintln(os.Stderr, "error: failed to write source output")
				return 1
			}
		case vmexecwire.StderrFrame:
			if _, err := os.Stderr.Write(value.Data); err != nil {
				return 1
			}
		case vmexecwire.ResultFrame:
			exitCode = value.ExitCode
		case vmexecwire.ErrorFrame:
			terminalError = true
		}
	}
}

func parseRemoteRequest(args []string) (vmexecwire.SourceRequest, error) {
	if len(args) < 5 || args[0] != "--user" || args[3] != "--" {
		return vmexecwire.SourceRequest{}, fmt.Errorf("invalid remote arguments")
	}
	user := args[1]
	if user == "" || user == "root" {
		return vmexecwire.SourceRequest{}, fmt.Errorf("remote user must be non-root")
	}
	destination := args[2]
	command := strings.Join(args[4:], " ")
	requestID, err := vmexecwire.NewRequestID()
	if err != nil {
		return vmexecwire.SourceRequest{}, err
	}
	request := vmexecwire.SourceRequest{
		RequestID:   requestID,
		Destination: destination,
		TargetUser:  user,
		Command:     command,
	}
	if err := request.Validate(); err != nil {
		return vmexecwire.SourceRequest{}, err
	}
	return request, nil
}
