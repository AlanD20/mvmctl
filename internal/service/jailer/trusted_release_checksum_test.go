package jailer

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

const testTrustedReleaseDigestText = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestParseTrustedReleaseChecksumAcceptsExactSidecar(t *testing.T) {
	t.Parallel()

	tests := []releaseSlot{
		{version: "1.16.1", architecture: "x86_64"},
		{version: "1.16.1", architecture: "aarch64"},
	}
	for _, slot := range tests {
		t.Run(slot.architecture, func(t *testing.T) {
			t.Parallel()

			source, err := newTrustedReleaseSource(slot)
			require.NoError(t, err)
			raw := []byte(testTrustedReleaseDigestText + "  " + source.archiveName + "\n")

			got, err := parseTrustedReleaseChecksum(raw, source)
			require.NoError(t, err)
			var want trustedReleaseArchiveDigest
			for index := range want {
				want[index] = byte(index)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("parseTrustedReleaseChecksum() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestParseTrustedReleaseChecksumRejectsNonCanonicalSidecar(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	valid := testTrustedReleaseDigestText + "  " + source.archiveName + "\n"

	tests := map[string]string{
		"empty":                 "",
		"short digest":          testTrustedReleaseDigestText[:62] + "  " + source.archiveName + "\n",
		"uppercase digest":      strings.ToUpper(testTrustedReleaseDigestText) + "  " + source.archiveName + "\n",
		"non-hex digest":        "g" + testTrustedReleaseDigestText[1:] + "  " + source.archiveName + "\n",
		"single separator":      testTrustedReleaseDigestText + " " + source.archiveName + "\n",
		"tab separator":         testTrustedReleaseDigestText + "\t " + source.archiveName + "\n",
		"GNU binary marker":     testTrustedReleaseDigestText + " *" + source.archiveName + "\n",
		"alternate filename":    testTrustedReleaseDigestText + "  other.tgz\n",
		"missing newline":       strings.TrimSuffix(valid, "\n"),
		"CRLF":                  strings.TrimSuffix(valid, "\n") + "\r\n",
		"additional line":       valid + valid,
		"body above hard limit": strings.Repeat("x", trustedReleaseChecksumMaxBytes+1),
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, parseErr := parseTrustedReleaseChecksum([]byte(raw), source)
			require.Error(t, parseErr)
			domainErr := errs.AsDomainError(parseErr)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
		})
	}
}

func TestParseTrustedReleaseChecksumRejectsForgedSource(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	raw := []byte(testTrustedReleaseDigestText + "  " + source.archiveName + "\n")
	source.archiveName = "attacker-controlled.tgz"

	_, err = parseTrustedReleaseChecksum(raw, source)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
}

func TestTrustedReleaseChecksumAuthorityUsesClosedTransportPolicy(t *testing.T) {
	t.Parallel()

	authority := newTrustedReleaseChecksumAuthority()
	require.NotNil(t, authority)
	require.NotNil(t, authority.client)
	assert.Equal(t, trustedReleaseChecksumTotalTimeout, authority.client.Timeout)
	assert.NotNil(t, authority.client.CheckRedirect)

	transport, ok := authority.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.NotNil(t, transport.DialContext)
	assert.True(t, transport.DisableCompression)
	assert.True(t, transport.DisableKeepAlives)
	assert.False(t, transport.ForceAttemptHTTP2)
	assert.Equal(t, 1, transport.MaxConnsPerHost)
	assert.Equal(t, trustedReleaseChecksumPhaseTimeout, transport.TLSHandshakeTimeout)
	assert.Equal(t, trustedReleaseChecksumPhaseTimeout, transport.ResponseHeaderTimeout)
	assert.Equal(t, int64(trustedReleaseChecksumMaxHeaderBytes), transport.MaxResponseHeaderBytes)
	require.NotNil(t, transport.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
}

func TestTrustedReleaseChecksumRedirectPolicy(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	initial, err := http.NewRequest(http.MethodGet, source.checksumURL, nil)
	require.NoError(t, err)
	asset, err := http.NewRequest(
		http.MethodGet,
		"https://release-assets.githubusercontent.com/github-production-release-asset/checksum?token=opaque",
		nil,
	)
	require.NoError(t, err)

	tests := []struct {
		name    string
		rawURL  string
		via     []*http.Request
		wantErr bool
	}{
		{
			name:   "single HTTPS asset redirect",
			rawURL: asset.URL.String(),
			via:    []*http.Request{initial},
		},
		{
			name:    "HTTP redirect",
			rawURL:  "http://release-assets.githubusercontent.com/asset",
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name:    "foreign host",
			rawURL:  "https://example.com/asset",
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name:    "asset host with port",
			rawURL:  "https://release-assets.githubusercontent.com:443/asset",
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name:    "userinfo",
			rawURL:  "https://user@release-assets.githubusercontent.com/asset",
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name:    "fragment",
			rawURL:  "https://release-assets.githubusercontent.com/asset#fragment",
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name:    "second redirect",
			rawURL:  asset.URL.String(),
			via:     []*http.Request{initial, asset},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request, requestErr := http.NewRequest(http.MethodGet, tc.rawURL, nil)
			require.NoError(t, requestErr)
			redirectErr := checkTrustedReleaseChecksumRedirect(request, tc.via)
			if tc.wantErr {
				assert.Error(t, redirectErr)
				return
			}
			assert.NoError(t, redirectErr)
		})
	}
}

func TestTrustedReleaseChecksumAuthorityFetchesDerivedSidecar(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	body := newTrackingChecksumBody(testTrustedReleaseDigestText+"  "+source.archiveName+"\n", nil, nil)
	var captured *http.Request
	roundTripper := trustedReleaseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: int64(len(testTrustedReleaseDigestText + "  " + source.archiveName + "\n")),
			Body:          body,
			Request:       request,
		}, nil
	})
	authority := testTrustedReleaseChecksumAuthority(roundTripper)

	got, err := authority.fetch(context.Background(), source)
	require.NoError(t, err)
	var want trustedReleaseArchiveDigest
	for index := range want {
		want[index] = byte(index)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fetch() digest mismatch (-want +got):\n%s", diff)
	}
	require.NotNil(t, captured)
	assert.Equal(t, http.MethodGet, captured.Method)
	assert.Equal(t, source.checksumURL, captured.URL.String())
	assert.Equal(t, trustedReleaseChecksumAccept, captured.Header.Get("Accept"))
	assert.Equal(t, "identity", captured.Header.Get("Accept-Encoding"))
	assert.Equal(t, "no-store", captured.Header.Get("Cache-Control"))
	assert.Equal(t, trustedReleaseChecksumUserAgent, captured.Header.Get("User-Agent"))
	assert.Empty(t, captured.Header.Get("Authorization"))
	assert.Empty(t, captured.Header.Get("Cookie"))
	assert.True(t, body.closed)
}

func TestTrustedReleaseChecksumAuthorityRejectsForgedSourceBeforeRequest(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	source.checksumURL = "https://example.com/attacker-controlled"
	called := false
	authority := testTrustedReleaseChecksumAuthority(trustedReleaseRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("request must not run")
		},
	))

	_, err = authority.fetch(context.Background(), source)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeBinaryUntrusted, domainErr.Code)
	assert.False(t, called)
}

func TestTrustedReleaseChecksumAuthorityBoundsResponse(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	valid := testTrustedReleaseDigestText + "  " + source.archiveName + "\n"

	tests := []struct {
		name          string
		status        int
		contentLength int64
		body          string
		readErr       error
		closeErr      error
		wantCode      errs.Code
	}{
		{
			name:          "non-OK status",
			status:        http.StatusNotFound,
			contentLength: int64(len("missing")),
			body:          "missing",
			wantCode:      errs.CodeDownloadFailed,
		},
		{
			name:          "declared body above limit",
			status:        http.StatusOK,
			contentLength: trustedReleaseChecksumMaxBytes + 1,
			wantCode:      errs.CodeDownloadFailed,
		},
		{
			name:          "invalid negative content length",
			status:        http.StatusOK,
			contentLength: -2,
			wantCode:      errs.CodeDownloadFailed,
		},
		{
			name:          "streamed body above limit",
			status:        http.StatusOK,
			contentLength: -1,
			body:          strings.Repeat("x", trustedReleaseChecksumMaxBytes+1),
			wantCode:      errs.CodeDownloadFailed,
		},
		{
			name:          "body read failure",
			status:        http.StatusOK,
			contentLength: -1,
			readErr:       errors.New("read failed"),
			wantCode:      errs.CodeDownloadFailed,
		},
		{
			name:          "body close failure",
			status:        http.StatusOK,
			contentLength: int64(len(valid)),
			body:          valid,
			closeErr:      errors.New("close failed"),
			wantCode:      errs.CodeDownloadFailed,
		},
		{
			name:          "malformed sidecar",
			status:        http.StatusOK,
			contentLength: int64(len("not-a-checksum\n")),
			body:          "not-a-checksum\n",
			wantCode:      errs.CodeBinaryUntrusted,
		},
		{
			name:          "malformed sidecar with close failure",
			status:        http.StatusOK,
			contentLength: int64(len("not-a-checksum\n")),
			body:          "not-a-checksum\n",
			closeErr:      errors.New("close failed"),
			wantCode:      errs.CodeBinaryUntrusted,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			body := newTrackingChecksumBody(tc.body, tc.readErr, tc.closeErr)
			calls := 0
			authority := testTrustedReleaseChecksumAuthority(trustedReleaseRoundTripFunc(
				func(request *http.Request) (*http.Response, error) {
					calls++
					return &http.Response{
						StatusCode:    tc.status,
						ContentLength: tc.contentLength,
						Body:          body,
						Request:       request,
					}, nil
				},
			))

			_, fetchErr := authority.fetch(context.Background(), source)
			require.Error(t, fetchErr)
			domainErr := errs.AsDomainError(fetchErr)
			require.NotNil(t, domainErr)
			assert.Equal(t, tc.wantCode, domainErr.Code)
			assert.Equal(t, 1, calls)
			assert.True(t, body.closed)
		})
	}
}

func TestTrustedReleaseChecksumAuthorityDoesNotRetryTransportFailure(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	calls := 0
	authority := testTrustedReleaseChecksumAuthority(trustedReleaseRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("network unavailable")
		},
	))

	_, err = authority.fetch(context.Background(), source)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeDownloadFailed, domainErr.Code)
	assert.Equal(t, 1, calls)
}

func TestTrustedReleaseChecksumAuthorityHonorsCanceledContextBeforeRequest(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	called := false
	authority := testTrustedReleaseChecksumAuthority(trustedReleaseRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			called = true
			return nil, errors.New("request must not run")
		},
	))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = authority.fetch(ctx, source)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, called)
}

func TestTrustedReleaseChecksumAuthorityPropagatesInFlightCancellation(t *testing.T) {
	t.Parallel()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	started := make(chan struct{})
	authority := testTrustedReleaseChecksumAuthority(trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			close(started)
			<-request.Context().Done()
			return nil, request.Context().Err()
		},
	))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, fetchErr := authority.fetch(ctx, source)
		result <- fetchErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("checksum request did not enter the transport")
	}
	cancel()

	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("checksum request did not stop after cancellation")
	}
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

type trustedReleaseRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip trustedReleaseRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type trackingChecksumBody struct {
	reader   io.Reader
	readErr  error
	closeErr error
	closed   bool
}

func newTrackingChecksumBody(value string, readErr, closeErr error) *trackingChecksumBody {
	return &trackingChecksumBody{reader: strings.NewReader(value), readErr: readErr, closeErr: closeErr}
}

func (body *trackingChecksumBody) Read(buffer []byte) (int, error) {
	count, err := body.reader.Read(buffer)
	if err == io.EOF && body.readErr != nil {
		return count, body.readErr
	}
	return count, err
}

func (body *trackingChecksumBody) Close() error {
	body.closed = true
	return body.closeErr
}

func testTrustedReleaseChecksumAuthority(roundTripper http.RoundTripper) *trustedReleaseChecksumAuthority {
	return &trustedReleaseChecksumAuthority{client: &http.Client{
		Transport:     roundTripper,
		CheckRedirect: checkTrustedReleaseChecksumRedirect,
		Timeout:       2 * time.Second,
	}}
}
