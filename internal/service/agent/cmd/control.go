package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/guestcontrolwire"
)

const controlExchangeTimeout = 30 * time.Second

func runControlSubcommand(args []string) int {
	request, err := parseControlRequest(args)
	if err != nil {
		fmt.Fprintln(os.Stderr,
			"usage: mvm-agent control floating-ip {inspect,associate,renew,release} [--generation N] RESOURCE")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlExchangeTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", infra.GuestControlLocalSocketPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: guest control is unavailable")
		return 1
	}
	defer conn.Close()
	if err := runControlExchange(ctx, conn, request, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

func parseControlRequest(args []string) (guestcontrolwire.Request, error) {
	if len(args) != 3 && len(args) != 5 || len(args) < 2 || args[0] != "floating-ip" {
		return guestcontrolwire.Request{}, errors.New("invalid control arguments")
	}
	operation := guestcontrolwire.Operation(args[1])
	resource := args[2]
	var generation uint64
	if len(args) == 5 {
		if args[2] != "--generation" {
			return guestcontrolwire.Request{}, errors.New("invalid generation flag")
		}
		parsed, err := strconv.ParseUint(args[3], 10, 64)
		if err != nil || strconv.FormatUint(parsed, 10) != args[3] {
			return guestcontrolwire.Request{}, errors.New("invalid generation")
		}
		generation = parsed
		resource = args[4]
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return guestcontrolwire.Request{}, errors.New("control request identity unavailable")
	}
	request := guestcontrolwire.Request{RequestID: id.String(), Operation: operation,
		Input: guestcontrolwire.FloatingIPInput{ResourceID: resource, ExpectedGeneration: generation}}
	if err := request.Validate(); err != nil {
		return guestcontrolwire.Request{}, errors.New("invalid control request")
	}
	if operation != guestcontrolwire.Inspect && generation == 0 {
		return guestcontrolwire.Request{}, errors.New("mutation requires generation")
	}
	return request, nil
}

func runControlExchange(ctx context.Context, conn net.Conn, request guestcontrolwire.Request, output io.Writer) error {
	if ctx == nil || conn == nil || output == nil {
		return errors.New("control exchange is unavailable")
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return errors.New("control exchange requires a deadline")
	}
	local, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("control exchange requires a Unix socket")
	}
	if err := local.SetDeadline(deadline); err != nil {
		return errors.New("control transport deadline failed")
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := guestcontrolwire.EncodeRequest(ctx, conn, request); err != nil {
		return errors.New("control request could not be sent")
	}
	if err := local.CloseWrite(); err != nil {
		return errors.New("control request completion failed")
	}
	response, err := guestcontrolwire.DecodeResponse(ctx, conn, request)
	if err != nil {
		return errors.New("control outcome is unknown; inspect before retrying")
	}
	var trailing [1]byte
	if n, err := conn.Read(trailing[:]); n != 0 || !errors.Is(err, io.EOF) {
		return errors.New("control response completion is unknown; inspect before retrying")
	}
	if response.Failure != nil {
		return fmt.Errorf("guest control request failed: %s", response.Failure.Code)
	}
	if err := json.NewEncoder(output).Encode(response.Result); err != nil {
		return errors.New("control result output failed")
	}
	return nil
}
