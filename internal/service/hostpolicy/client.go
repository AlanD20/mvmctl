package hostpolicy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"syscall"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/lib/system"
)

type policyExchange interface {
	Exchange(context.Context, []byte) policyExchangeOutcome
}

type policyExchangeOutcome struct {
	response    []byte
	responseEOF bool
	started     bool
	diagnostic  error
}

type fixedPolicyExchange struct{}

func (fixedPolicyExchange) Exchange(ctx context.Context, frame []byte) policyExchangeOutcome {
	request, err := system.NewPrivilegedControlRequest(ObserveAction, frame, 0, nil)
	if err != nil {
		return policyExchangeOutcome{diagnostic: err}
	}
	outcome := (system.PrivilegedControlTransport{}).Exchange(ctx, request)
	var diagnostic error
	if err := outcome.Diagnostic(); err != nil {
		diagnostic = err
	}
	return policyExchangeOutcome{
		response: outcome.Response(), responseEOF: outcome.ResponseEOF(),
		started: outcome.ProcessStarted(), diagnostic: diagnostic,
	}
}

// Client observes only the managed policy through the installed privileged dispatcher.
type Client struct {
	exchange policyExchange
	lstat    func(string) (os.FileInfo, error)
}

// NewClient binds the closed policy capability to the fixed transport.
func NewClient() *Client {
	return &Client{exchange: fixedPolicyExchange{}, lstat: os.Lstat}
}

// Observe avoids a privileged request only when the caller can confirm ordinary absence.
func (client *Client) Observe(ctx context.Context) (ObserveResult, error) {
	if ctx == nil || client == nil || client.exchange == nil || client.lstat == nil {
		return ObserveResult{}, policyError("managed host policy client is inactive", nil)
	}
	if err := ctx.Err(); err != nil {
		return ObserveResult{}, policyError("observe managed host policy", err)
	}
	if _, err := client.lstat(infra.SudoersDropInPath()); errors.Is(err, os.ErrNotExist) &&
		ordinaryPolicyAbsence(client.lstat) {
		exists := false
		return ObserveResult{Exists: &exists}, nil
	}
	var frame bytes.Buffer
	if err := privilegedwire.EncodeRequest(ctx, &frame, ObserveAction, ObserveRequest{}, 0); err != nil {
		return ObserveResult{}, err
	}
	outcome := client.exchange.Exchange(ctx, frame.Bytes())
	if !outcome.started || !outcome.responseEOF {
		return ObserveResult{}, policyError(
			"managed host policy response did not reach authoritative EOF",
			outcome.diagnostic,
		)
	}
	response, err := privilegedwire.DecodeResponse[ObserveResult](
		context.WithoutCancel(ctx), bytes.NewReader(outcome.response), ObserveAction)
	if err != nil {
		return ObserveResult{}, policyError(
			"managed host policy response is invalid",
			errors.Join(err, outcome.diagnostic),
		)
	}
	if err := response.OperationError(); err != nil {
		return ObserveResult{}, err
	}
	result := response.Result()
	if err := result.Validate(); err != nil {
		return ObserveResult{}, err
	}
	return result, nil
}

// Absence beneath an unsafe or inaccessible ancestor is not an ordinary absence observation.
func ordinaryPolicyAbsence(lstat func(string) (os.FileInfo, error)) bool {
	for _, path := range [...]string{"/", "/etc", "/etc/sudoers.d"} {
		info, err := lstat(path)
		if err != nil {
			return errors.Is(err, os.ErrNotExist)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || stat.Uid != 0 || stat.Gid != 0 || info.Mode().Perm()&0022 != 0 {
			return false
		}
	}
	return true
}
