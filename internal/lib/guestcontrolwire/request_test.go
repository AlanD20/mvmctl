package guestcontrolwire_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/guestcontrolwire"
	"mvmctl/pkg/errs"
)

const validBody = `{"schema_version":1,"request_id":"09a92b07-065a-4fd9-878a-345b096d53dd",` +
	`"feature":"floating_ip","operation":"associate",` +
	`"input":{"resource_id":"c598985a13454b019f7c4c07c0aaa1ec","expected_generation":7}}`

func TestRequestDecodesIndependentFrameAndPreservesNextFrame(t *testing.T) {
	stream := bytes.NewReader(
		append(frame(validBody), frame(strings.Replace(validBody, `"associate"`, `"renew"`, 1))...),
	)
	want := guestcontrolwire.Request{
		RequestID: "09a92b07-065a-4fd9-878a-345b096d53dd",
		Operation: guestcontrolwire.Associate,
		Input: guestcontrolwire.FloatingIPInput{
			ResourceID: "c598985a13454b019f7c4c07c0aaa1ec", ExpectedGeneration: 7,
		},
	}
	got, err := guestcontrolwire.DecodeRequest(context.Background(), stream)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("decoded request (-want +got):\n%s", diff)
	}
	want.Operation = guestcontrolwire.Renew
	got, err = guestcontrolwire.DecodeRequest(context.Background(), stream)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("second request (-want +got):\n%s", diff)
	}
	if stream.Len() != 0 {
		t.Fatal("decoder left bytes after two complete frames")
	}
}

func TestRequestRejectsAmbiguousOrExtendedInputs(t *testing.T) {
	cases := map[string]string{
		"forged caller identity": strings.Replace(validBody, `"feature":`, `"vm_id":"victim","feature":`, 1),
		"raw command": strings.Replace(
			validBody,
			`"expected_generation":7`,
			`"expected_generation":7,"command":"id"`,
			1,
		),
		"duplicate operation": strings.Replace(validBody, `"operation":`, `"operation":"release","operation":`, 1),
		"duplicate nested generation": strings.Replace(validBody, `"expected_generation":7`,
			`"expected_generation":6,"expected_generation":7`, 1),
		"case alias":          strings.Replace(validBody, `"operation":`, `"Operation":`, 1),
		"escaped duplicate":   strings.Replace(validBody, `"operation":`, `"oper\u0061tion":"release","operation":`, 1),
		"null generation":     strings.Replace(validBody, `"expected_generation":7`, `"expected_generation":null`, 1),
		"negative generation": strings.Replace(validBody, `"expected_generation":7`, `"expected_generation":-1`, 1),
		"overflow generation": strings.Replace(validBody, `"expected_generation":7`,
			`"expected_generation":18446744073709551616`, 1),
		"fractional generation": strings.Replace(validBody, `"expected_generation":7`, `"expected_generation":7.5`, 1),
		"missing generation":    strings.Replace(validBody, `,"expected_generation":7`, ``, 1),
		"null input": strings.Replace(validBody,
			`{"resource_id":"c598985a13454b019f7c4c07c0aaa1ec","expected_generation":7}`, `null`, 1),
		"unsupported feature": strings.Replace(validBody, `"floating_ip"`, `"shell"`, 1),
		"unsupported method":  strings.Replace(validBody, `"associate"`, `"allocate"`, 1),
		"unsupported schema":  strings.Replace(validBody, `"schema_version":1`, `"schema_version":2`, 1),
		"invalid request ID":  strings.Replace(validBody, `09a92b07-065a-4fd9-878a-345b096d53dd`, `caller-session`, 1),
		"UUIDv1":              strings.Replace(validBody, `-4fd9-`, `-1fd9-`, 1),
		"UUIDv5":              strings.Replace(validBody, `-4fd9-`, `-5fd9-`, 1),
		"UUID variant":        strings.Replace(validBody, `-878a-`, `-778a-`, 1),
		"uppercase UUID":      strings.Replace(validBody, `09a92b07`, `09A92B07`, 1),
		"resource path":       strings.Replace(validBody, `c598985a13454b019f7c4c07c0aaa1ec`, `/etc/passwd`, 1),
		"malformed JSON":      validBody[:len(validBody)-1] + `,}`,
		"trailing JSON":       validBody + `{}`,
		"array envelope":      `[` + validBody + `]`,
		"invalid UTF8":        strings.Replace(validBody, `floating_ip`, "floating_\xffip", 1),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := guestcontrolwire.DecodeRequest(context.Background(), bytes.NewReader(frame(body)))
			require.Error(t, err)
			domain := errs.AsDomainError(err)
			require.NotNil(t, domain)
			if domain.Code != errs.CodeValidationFailed {
				t.Errorf("error code = %s, want validation.failed", domain.Code)
			}
		})
	}
}

func TestRequestAdmitsAllMethodsAndExactGenerationBounds(t *testing.T) {
	cases := map[string]struct {
		operation  guestcontrolwire.Operation
		generation uint64
		number     string
	}{
		"inspect zero":          {guestcontrolwire.Inspect, 0, "0"},
		"associate maximum":     {guestcontrolwire.Associate, 18446744073709551615, "18446744073709551615"},
		"renew precise integer": {guestcontrolwire.Renew, 9007199254740993, "9007199254740993"},
		"release maximum":       {guestcontrolwire.Release, 18446744073709551615, "18446744073709551615"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(validBody, `"associate"`, `"`+string(tc.operation)+`"`, 1)
			body = strings.Replace(body, `"expected_generation":7`, `"expected_generation":`+tc.number, 1)
			got, err := guestcontrolwire.DecodeRequest(context.Background(), bytes.NewReader(frame(body)))
			require.NoError(t, err)
			want := guestcontrolwire.Request{
				RequestID: "09a92b07-065a-4fd9-878a-345b096d53dd", Operation: tc.operation,
				Input: guestcontrolwire.FloatingIPInput{
					ResourceID: "c598985a13454b019f7c4c07c0aaa1ec", ExpectedGeneration: tc.generation,
				},
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("decoded request (-want +got):\n%s", diff)
			}
			var encoded bytes.Buffer
			require.NoError(t, guestcontrolwire.EncodeRequest(context.Background(), &encoded, want))
			if diff := cmp.Diff(frame(body), encoded.Bytes()); diff != "" {
				t.Fatalf("encoded request (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRequestFrameBoundsAndTruncation(t *testing.T) {
	oversize := frame(validBody)
	binary.BigEndian.PutUint32(oversize[10:14], 16385)
	empty := frame("")
	wrongMagic := frame(validBody)
	wrongMagic[0] = 'X'
	wrongVersion := frame(validBody)
	wrongVersion[9] = 2
	cases := map[string][]byte{
		"oversized length without body": oversize[:14],
		"empty body":                    empty,
		"wrong magic":                   wrongMagic,
		"wrong version":                 wrongVersion,
		"short prefix":                  frame(validBody)[:13],
		"short body":                    frame(validBody)[:len(frame(validBody))-1],
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := guestcontrolwire.DecodeRequest(context.Background(), bytes.NewReader(raw))
			require.Error(t, err)
		})
	}
	_, err := guestcontrolwire.DecodeRequest(context.Background(),
		bytes.NewReader(frame(validBody+strings.Repeat(" ", 16384-len(validBody)))))
	require.NoError(t, err, "exact maximum body must be accepted")
}

func TestRequestEncodingMatchesLiteralContract(t *testing.T) {
	request := guestcontrolwire.Request{
		RequestID: "09a92b07-065a-4fd9-878a-345b096d53dd",
		Operation: guestcontrolwire.Associate,
		Input: guestcontrolwire.FloatingIPInput{
			ResourceID: "c598985a13454b019f7c4c07c0aaa1ec", ExpectedGeneration: 7,
		},
	}
	var encoded bytes.Buffer
	require.NoError(t, guestcontrolwire.EncodeRequest(context.Background(), &encoded, request))
	if diff := cmp.Diff(frame(validBody), encoded.Bytes()); diff != "" {
		t.Fatalf("encoded frame (-want +got):\n%s", diff)
	}
	var fragmented partialWriter
	require.NoError(t, guestcontrolwire.EncodeRequest(context.Background(), &fragmented, request))
	if diff := cmp.Diff(frame(validBody), fragmented.Bytes()); diff != "" {
		t.Fatalf("partial-write frame (-want +got):\n%s", diff)
	}
	err := guestcontrolwire.EncodeRequest(context.Background(), zeroWriter{}, request)
	if !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero-progress write = %v, want io.ErrShortWrite", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var untouched bytes.Buffer
	err = guestcontrolwire.EncodeRequest(ctx, &untouched, request)
	if !errors.Is(err, context.Canceled) || untouched.Len() != 0 {
		t.Fatalf("cancelled encode = %v, bytes = %d", err, untouched.Len())
	}
	_, err = guestcontrolwire.DecodeRequest(ctx, bytes.NewReader(frame(validBody)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled decode = %v, want context.Canceled", err)
	}
}

func frame(body string) []byte {
	// Construct the documented wire layout independently of production encoders.
	prefix := append([]byte("MVMGCTL1"), 0, 1, 0, 0, 0, 0)
	binary.BigEndian.PutUint32(prefix[10:14], uint32(len(body)))
	return append(prefix, body...)
}

type partialWriter struct{ bytes.Buffer }

func (writer *partialWriter) Write(value []byte) (int, error) {
	return writer.Buffer.Write(value[:min(3, len(value))])
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }
