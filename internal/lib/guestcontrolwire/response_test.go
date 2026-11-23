package guestcontrolwire_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/pkg/errs"
)

const responseBody = `{"schema_version":1,"request_id":"09a92b07-065a-4fd9-878a-345b096d53dd",` +
	`"feature":"floating_ip","operation":"associate","result":{` +
	`"lifetime":"finite","resource_id":"c598985a13454b019f7c4c07c0aaa1ec","ipv4":"10.25.0.100",` +
	`"generation":8,"ownership":"self","lease_remaining_ms":15000}}`

func responseRequest() guestcontrolwire.Request {
	return guestcontrolwire.Request{
		RequestID: "09a92b07-065a-4fd9-878a-345b096d53dd", Operation: guestcontrolwire.Associate,
		Input: guestcontrolwire.FloatingIPInput{
			ResourceID: "c598985a13454b019f7c4c07c0aaa1ec", ExpectedGeneration: 7,
		},
	}
}

func TestResponseBindsIndependentResultAndPreservesFollowingFrame(t *testing.T) {
	request := responseRequest()
	want := guestcontrolwire.Response{
		RequestID: request.RequestID, Operation: guestcontrolwire.Associate,
		Result: &guestcontrolwire.FloatingIPResult{
			Lifetime:   guestcontrolwire.Finite,
			ResourceID: request.Input.ResourceID, IPv4: "10.25.0.100", Generation: 8,
			Ownership: guestcontrolwire.OwnedBySelf, LeaseRemainingMillis: 15000,
		},
	}
	stream := bytes.NewReader(append(frame(responseBody), frame(validBody)...))
	got, err := guestcontrolwire.DecodeResponse(context.Background(), stream, request)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("response (-want +got):\n%s", diff)
	}
	next, err := guestcontrolwire.DecodeRequest(context.Background(), stream)
	require.NoError(t, err)
	if diff := cmp.Diff(request, next); diff != "" {
		t.Fatalf("following request (-want +got):\n%s", diff)
	}
	var encoded bytes.Buffer
	require.NoError(t, guestcontrolwire.EncodeResponse(context.Background(), &encoded, request, want))
	if diff := cmp.Diff(frame(responseBody), encoded.Bytes()); diff != "" {
		t.Fatalf("response frame (-want +got):\n%s", diff)
	}
}

func TestResponseRejectsUnboundAmbiguousAndUnsafeResults(t *testing.T) {
	replace := func(old, replacement string) string { return strings.Replace(responseBody, old, replacement, 1) }
	cases := map[string]string{
		"wrong request":     replace("09a92b07-065a-4fd9-878a-345b096d53dd", "09a92b07-065a-4fd9-878a-345b096d53de"),
		"wrong operation":   replace(`"associate"`, `"renew"`),
		"wrong resource":    replace("c598985a13454b019f7c4c07c0aaa1ec", "c598985a13454b019f7c4c07c0aaa1ed"),
		"duplicate result":  replace(`"result":`, `"result":{},"result":`),
		"duplicate nested":  replace(`"generation":8`, `"generation":7,"generation":8`),
		"case alias":        replace(`"ownership":`, `"Ownership":`),
		"escaped duplicate": replace(`"ownership":"self"`, `"ownership":"other","own\u0065rship":"self"`),
		"null scalar":       replace(`"generation":8`, `"generation":null`),
		"null result": strings.Replace(responseBody, responseBody[strings.Index(responseBody, `"result":`):],
			`"result":null}`, 1),
		"both union arms":      replace(`"result":`, `"error":{"code":"denied"},"result":`),
		"unknown field":        replace(`"ownership":`, `"owner_uid":1000,"ownership":`),
		"missing field":        replace(`,"ownership":"self"`, ``),
		"unknown state":        replace(`"ownership":"self"`, `"ownership":"operator"`),
		"not self":             replace(`"ownership":"self"`, `"ownership":"other"`),
		"expired self":         replace(`"lease_remaining_ms":15000`, `"lease_remaining_ms":0`),
		"negative duration":    replace(`"lease_remaining_ms":15000`, `"lease_remaining_ms":-1`),
		"duration overflow":    replace(`"lease_remaining_ms":15000`, `"lease_remaining_ms":9223372036855`),
		"generation zero":      replace(`"generation":8`, `"generation":0`),
		"stale generation":     replace(`"generation":8`, `"generation":6`),
		"public address":       replace("10.25.0.100", "8.8.8.8"),
		"noncanonical address": replace("10.25.0.100", "010.25.0.100"),
		"mapped address":       replace("10.25.0.100", "::ffff:10.25.0.100"),
		"unsupported version":  replace(`"schema_version":1`, `"schema_version":2`),
		"unsupported feature":  replace(`"floating_ip"`, `"shell"`),
		"invalid UTF8":         replace("self", "s\xfflf"),
		"trailing JSON":        responseBody + `{}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := guestcontrolwire.DecodeResponse(
				context.Background(),
				bytes.NewReader(frame(body)),
				responseRequest(),
			)
			require.Error(t, err)
			require.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestResponseOwnershipTransitions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		operation  guestcontrolwire.Operation
		generation uint64
		ownership  guestcontrolwire.Ownership
		remaining  uint64
		valid      bool
	}{
		{"inspect another", guestcontrolwire.Inspect, 8, guestcontrolwire.OwnedByOther, 15000, true},
		{"inspect unassociated", guestcontrolwire.Inspect, 1, guestcontrolwire.Unassociated, 0, true},
		{"renew same generation", guestcontrolwire.Renew, 7, guestcontrolwire.OwnedBySelf, 15000, true},
		{"renew different generation", guestcontrolwire.Renew, 8, guestcontrolwire.OwnedBySelf, 15000, false},
		{"release", guestcontrolwire.Release, 8, guestcontrolwire.Unassociated, 0, true},
		{"release still owned", guestcontrolwire.Release, 8, guestcontrolwire.OwnedBySelf, 15000, false},
		{"unassociated reports lease", guestcontrolwire.Inspect, 8, guestcontrolwire.Unassociated, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := responseRequest()
			request.Operation = tc.operation
			body := strings.Replace(responseBody, `"associate"`, fmt.Sprintf("%q", tc.operation), 1)
			body = strings.Replace(body, `"generation":8`, fmt.Sprintf(`"generation":%d`, tc.generation), 1)
			body = strings.Replace(body, `"ownership":"self"`, fmt.Sprintf(`"ownership":%q`, tc.ownership), 1)
			body = strings.Replace(
				body,
				`"lease_remaining_ms":15000`,
				fmt.Sprintf(`"lease_remaining_ms":%d`, tc.remaining),
				1,
			)
			lifetime := guestcontrolwire.Finite
			if tc.ownership == guestcontrolwire.Unassociated {
				lifetime = ""
			}
			body = strings.Replace(body, `"lifetime":"finite"`, fmt.Sprintf(`"lifetime":%q`, lifetime), 1)
			got, err := guestcontrolwire.DecodeResponse(context.Background(), bytes.NewReader(frame(body)), request)
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			want := &guestcontrolwire.FloatingIPResult{
				Lifetime:   lifetime,
				ResourceID: request.Input.ResourceID, IPv4: "10.25.0.100", Generation: tc.generation,
				Ownership: tc.ownership, LeaseRemainingMillis: tc.remaining,
			}
			if diff := cmp.Diff(want, got.Result); diff != "" {
				t.Fatalf("result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResponseFailuresHaveNoFreeFormHostDetails(t *testing.T) {
	for _, code := range []guestcontrolwire.FailureCode{
		guestcontrolwire.FailureInvalid, guestcontrolwire.FailureDenied, guestcontrolwire.FailureConflict,
		guestcontrolwire.FailureUnavailable, guestcontrolwire.FailureInternal, guestcontrolwire.FailureOutcomeUnknown,
	} {
		t.Run(string(code), func(t *testing.T) {
			body := responseBody[:strings.Index(responseBody, `"result":`)] + `"error":{"code":"` + string(code) + `"}}`
			got, err := guestcontrolwire.DecodeResponse(
				context.Background(),
				bytes.NewReader(frame(body)),
				responseRequest(),
			)
			require.NoError(t, err)
			require.Nil(t, got.Result)
			require.Equal(t, code, got.Failure.Code)
			var encoded bytes.Buffer
			require.NoError(t, guestcontrolwire.EncodeResponse(context.Background(), &encoded, responseRequest(), got))
			require.Equal(t, frame(body), encoded.Bytes())
			for _, invalidBody := range []string{
				strings.Replace(body, `"code":`, `"message":"/var/lib/private token=secret","code":`, 1),
				strings.Replace(body, `"code":`, `"Code":`, 1),
				strings.Replace(body, `"code":`, `"code":"internal","code":`, 1),
				strings.Replace(body, `"`+string(code)+`"`, `"future_error"`, 1),
			} {
				_, err := guestcontrolwire.DecodeResponse(
					context.Background(),
					bytes.NewReader(frame(invalidBody)),
					responseRequest(),
				)
				require.Error(t, err)
			}
		})
	}
}
