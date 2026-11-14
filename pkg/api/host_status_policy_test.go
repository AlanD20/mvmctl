package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/service/hostpolicy"
	"mvmctl/pkg/errs"
)

type unavailableHostPolicy struct{ err error }

func (observer unavailableHostPolicy) Observe(context.Context) (hostpolicy.ObserveResult, error) {
	return hostpolicy.ObserveResult{}, observer.err
}

func TestHostStatusCheckRejectsUnconfirmedPolicy(t *testing.T) {
	for name, err := range map[string]error{
		"root unavailable":    errs.New(errs.CodePrivilegeRequired, "root observation unavailable"),
		"omitted observation": nil,
	} {
		t.Run(name, func(t *testing.T) {
			op := &Operation{hostPolicyObserver: unavailableHostPolicy{err: err}}
			status, statusErr := op.HostStatusCheck(t.Context())
			assert.Error(t, statusErr)
			assert.Nil(t, status, "unavailable authority must not produce a confirmed absence status")
			if err != nil {
				assert.ErrorIs(t, statusErr, err)
			}
		})
	}
}
