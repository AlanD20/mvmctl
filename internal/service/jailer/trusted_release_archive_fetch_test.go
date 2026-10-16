package jailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestTrustedReleaseArchiveFetcherUsesClosedTransportPolicy(t *testing.T) {
	t.Parallel()

	fetcher := newTrustedReleaseArchiveFetcher()
	require.NotNil(t, fetcher)
	require.NotNil(t, fetcher.client)
	assert.Nil(t, fetcher.client.Jar)
	assert.Equal(t, 5*time.Minute, fetcher.client.Timeout)
	assert.NotNil(t, fetcher.client.CheckRedirect)

	transport, ok := fetcher.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.NotNil(t, transport.DialContext)
	assert.Nil(t, transport.DialTLSContext)
	assert.True(t, transport.DisableCompression)
	assert.True(t, transport.DisableKeepAlives)
	assert.False(t, transport.ForceAttemptHTTP2)
	assert.Equal(t, 1, transport.MaxConnsPerHost)
	assert.Equal(t, 5*time.Second, transport.TLSHandshakeTimeout)
	assert.Equal(t, 5*time.Second, transport.ResponseHeaderTimeout)
	assert.Equal(t, int64(16*1024), transport.MaxResponseHeaderBytes)
	require.NotNil(t, transport.TLSClientConfig)
	assert.Equal(t, uint16(tls.VersionTLS12), transport.TLSClientConfig.MinVersion)
	assert.False(t, transport.TLSClientConfig.InsecureSkipVerify)
}

func TestTrustedReleaseArchiveFetcherFetchesDerivedArchive(t *testing.T) {
	t.Parallel()

	source := trustedReleaseArchiveFetchSource(t)
	payload := []byte("a")
	body := newTrackingArchiveBody(bytes.NewReader(payload), nil)
	var captured *http.Request
	fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			captured = request
			return trustedReleaseArchiveFetchResponse(
				request,
				http.StatusOK,
				"1",
				1,
				nil,
				body,
			), nil
		},
	))
	stage, _ := newTrustedReleaseArchiveStreamFixture(t)

	err := fetcher.fetch(t.Context(), source, stage, trustedReleaseArchiveDigestForTest(payload))
	require.NoError(t, err)
	require.NotNil(t, captured)
	assert.Equal(t, http.MethodGet, captured.Method)
	assert.Equal(
		t,
		"https://github.com/firecracker-microvm/firecracker/releases/download/v1.16.1/"+
			"firecracker-v1.16.1-x86_64.tgz",
		captured.URL.String(),
	)
	assert.Empty(t, captured.Host)
	wantHeaders := http.Header{
		"Accept":          []string{"application/octet-stream"},
		"Accept-Encoding": []string{"identity"},
		"Cache-Control":   []string{"no-store"},
		"User-Agent":      []string{"mvmctl-trusted-release/1"},
	}
	if diff := cmp.Diff(wantHeaders, captured.Header); diff != "" {
		t.Errorf("archive request headers mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, body.closeCalls)
	assert.Equal(t, trustedReleaseArchiveStageReady, stage.state)
	assert.Equal(t, uint64(1), stage.sizeBytes)
	if diff := cmp.Diff(trustedReleaseArchiveDigestForTest(payload), stage.archiveDigest); diff != "" {
		t.Errorf("stage archive digest mismatch (-want +got):\n%s", diff)
	}

	stored := make([]byte, len(payload))
	count, readErr := unix.Pread(stage.fd, stored, 0)
	require.NoError(t, readErr)
	assert.Equal(t, len(payload), count)
	if diff := cmp.Diff(payload, stored); diff != "" {
		t.Errorf("fetched archive content mismatch (-want +got):\n%s", diff)
	}
	offset, seekErr := unix.Seek(stage.fd, 0, unix.SEEK_CUR)
	require.NoError(t, seekErr)
	assert.Equal(t, int64(0), offset)
}

func TestTrustedReleaseArchiveFetcherRejectsBeforeNetwork(t *testing.T) {
	t.Parallel()

	validSource := trustedReleaseArchiveFetchSource(t)
	forgedSource := validSource
	forgedSource.archiveURL = "https://example.com/untrusted.tgz"

	tests := []struct {
		name      string
		ctx       func() context.Context
		source    trustedReleaseSource
		stage     func(*testing.T) *trustedReleaseArchiveStage
		wantCode  errs.Code
		wantClass errs.Class
		wantErr   error
		wantState trustedReleaseArchiveStageState
	}{
		{
			name:   "forged source",
			ctx:    context.Background,
			source: forgedSource,
			stage: func(t *testing.T) *trustedReleaseArchiveStage {
				t.Helper()
				return nil
			},
			wantCode:  errs.CodeBinaryUntrusted,
			wantClass: errs.ClassValidation,
		},
		{
			name:   "inactive stage",
			ctx:    context.Background,
			source: validSource,
			stage: func(t *testing.T) *trustedReleaseArchiveStage {
				t.Helper()
				return &trustedReleaseArchiveStage{fd: -1}
			},
			wantCode:  errs.CodeBinaryTrustedInstallFailed,
			wantClass: errs.ClassInternal,
			wantState: trustedReleaseArchiveStageEmpty,
		},
		{
			name:   "nonempty stage",
			ctx:    context.Background,
			source: validSource,
			stage: func(t *testing.T) *trustedReleaseArchiveStage {
				t.Helper()
				stage, _ := newTrustedReleaseArchiveStreamFixture(t)
				stage.state = trustedReleaseArchiveStageReady
				return stage
			},
			wantCode:  errs.CodeBinaryTrustedInstallFailed,
			wantClass: errs.ClassInternal,
			wantState: trustedReleaseArchiveStageReady,
		},
		{
			name: "canceled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			source: validSource,
			stage: func(t *testing.T) *trustedReleaseArchiveStage {
				t.Helper()
				stage, _ := newTrustedReleaseArchiveStreamFixture(t)
				return stage
			},
			wantCode:  errs.CodeDownloadFailed,
			wantClass: errs.ClassInternal,
			wantErr:   context.Canceled,
			wantState: trustedReleaseArchiveStageEmpty,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
				func(*http.Request) (*http.Response, error) {
					calls++
					return nil, errors.New("request must not run")
				},
			))
			stage := tc.stage(t)

			err := fetcher.fetch(tc.ctx(), tc.source, stage, trustedReleaseArchiveDigest{})
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			want := trustedReleaseArchiveFetchErrorContract{Code: tc.wantCode, Class: tc.wantClass}
			got := trustedReleaseArchiveFetchErrorContract{Code: domainErr.Code, Class: domainErr.Class}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("pre-network error contract mismatch (-want +got):\n%s", diff)
			}
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			}
			assert.Equal(t, 0, calls)
			if stage != nil {
				assert.Equal(t, tc.wantState, stage.state)
			}
		})
	}
}

func TestTrustedReleaseArchiveRedirectPolicy(t *testing.T) {
	t.Parallel()

	source := trustedReleaseArchiveFetchSource(t)
	initial, err := http.NewRequest(http.MethodGet, source.archiveURL, nil)
	require.NoError(t, err)
	assetURL := "https://release-assets.githubusercontent.com/github-production-release-asset/archive?token=opaque"
	asset, err := http.NewRequest(http.MethodGet, assetURL, nil)
	require.NoError(t, err)

	tests := []struct {
		name    string
		request func(*testing.T) *http.Request
		via     []*http.Request
		wantErr bool
	}{
		{
			name: "single HTTPS asset redirect with signed query",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return cloneTrustedReleaseArchiveRequest(t, asset)
			},
			via: []*http.Request{initial},
		},
		{
			name: "zero prior requests",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return cloneTrustedReleaseArchiveRequest(t, asset)
			},
			wantErr: true,
		},
		{
			name: "second redirect",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return cloneTrustedReleaseArchiveRequest(t, asset)
			},
			via:     []*http.Request{initial, asset},
			wantErr: true,
		},
		{
			name: "non-GET",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				request := cloneTrustedReleaseArchiveRequest(t, asset)
				request.Method = http.MethodPost
				return request
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "HTTP",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return newTrustedReleaseArchiveRequestForTest(
					t,
					"http://release-assets.githubusercontent.com/archive",
				)
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "foreign host",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return newTrustedReleaseArchiveRequestForTest(t, "https://example.com/archive")
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "case-variant host",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return newTrustedReleaseArchiveRequestForTest(
					t,
					"https://Release-Assets.Githubusercontent.Com/archive",
				)
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "host with port",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return newTrustedReleaseArchiveRequestForTest(
					t,
					"https://release-assets.githubusercontent.com:443/archive",
				)
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "userinfo",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return newTrustedReleaseArchiveRequestForTest(
					t,
					"https://user@release-assets.githubusercontent.com/archive",
				)
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "fragment",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return newTrustedReleaseArchiveRequestForTest(
					t,
					"https://release-assets.githubusercontent.com/archive#fragment",
				)
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "raw fragment",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				request := cloneTrustedReleaseArchiveRequest(t, asset)
				request.URL.RawFragment = "raw-fragment"
				return request
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
		{
			name: "opaque URL",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				request := cloneTrustedReleaseArchiveRequest(t, asset)
				request.URL.Opaque = "//release-assets.githubusercontent.com/archive"
				return request
			},
			via:     []*http.Request{initial},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			request := tc.request(t)
			request.Header.Set("Authorization", "Bearer must-be-stripped")
			request.Header.Set("Cookie", "must-be-stripped=1")
			request.Header.Set("X-Untrusted", "must-be-stripped")
			request.Host = "attacker.example"
			redirectErr := checkTrustedReleaseArchiveRedirect(request, tc.via)
			if tc.wantErr {
				assert.Error(t, redirectErr)
				return
			}
			require.NoError(t, redirectErr)
			assert.Empty(t, request.Host)
			wantHeaders := http.Header{
				"Accept":          []string{"application/octet-stream"},
				"Accept-Encoding": []string{"identity"},
				"Cache-Control":   []string{"no-store"},
				"User-Agent":      []string{"mvmctl-trusted-release/1"},
			}
			if diff := cmp.Diff(wantHeaders, request.Header); diff != "" {
				t.Errorf("redirect headers mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: Go copies original headers while constructing a redirect. The policy must replace that map after the
// redirect exists, so a future header added before policy evaluation cannot cross to the signed asset request.
func TestTrustedReleaseArchiveFetcherStripsContaminatedRedirectHeaders(t *testing.T) {
	t.Parallel()

	source := trustedReleaseArchiveFetchSource(t)
	payload := []byte("x")
	initialBody := newTrackingArchiveBody(strings.NewReader("redirect"), nil)
	finalBody := newTrackingArchiveBody(bytes.NewReader(payload), nil)
	calls := 0
	roundTripper := trustedReleaseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode:    http.StatusFound,
				ContentLength: int64(len("redirect")),
				Header: http.Header{
					"Location": []string{
						"https://release-assets.githubusercontent.com/archive?token=opaque",
					},
				},
				Body:    initialBody,
				Request: request,
			}, nil
		}
		wantHeaders := http.Header{
			"Accept":          []string{"application/octet-stream"},
			"Accept-Encoding": []string{"identity"},
			"Cache-Control":   []string{"no-store"},
			"User-Agent":      []string{"mvmctl-trusted-release/1"},
		}
		if diff := cmp.Diff(wantHeaders, request.Header); diff != "" {
			t.Errorf("redirected request headers mismatch (-want +got):\n%s", diff)
		}
		assert.Empty(t, request.Host)
		return trustedReleaseArchiveFetchResponse(request, http.StatusOK, "1", 1, nil, finalBody), nil
	})
	fetcher := testTrustedReleaseArchiveFetcher(roundTripper)
	basePolicy := fetcher.client.CheckRedirect
	fetcher.client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		request.Header.Set("Authorization", "Bearer injected-before-policy")
		request.Header.Set("Cookie", "injected-before-policy=1")
		request.Header.Set("Referer", "https://github.com/ambient")
		request.Header.Set("X-Untrusted", "injected-before-policy")
		request.Host = "attacker.example"
		return basePolicy(request, via)
	}
	stage, _ := newTrustedReleaseArchiveStreamFixture(t)

	err := fetcher.fetch(t.Context(), source, stage, trustedReleaseArchiveDigestForTest(payload))
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, 1, initialBody.closeCalls)
	assert.Equal(t, 1, finalBody.closeCalls)
}

func TestTrustedReleaseArchiveFetcherRejectsDisallowedRedirectAsDownloadFailure(t *testing.T) {
	t.Parallel()

	body := newTrackingArchiveBody(strings.NewReader("redirect"), nil)
	calls := 0
	fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode:    http.StatusFound,
				ContentLength: int64(len("redirect")),
				Header:        http.Header{"Location": []string{"https://example.com/archive"}},
				Body:          body,
				Request:       request,
			}, nil
		},
	))
	stage, _ := newTrustedReleaseArchiveStreamFixture(t)

	err := fetcher.fetch(t.Context(), trustedReleaseArchiveFetchSource(t), stage, trustedReleaseArchiveDigest{})
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	want := trustedReleaseArchiveFetchErrorContract{Code: errs.CodeDownloadFailed, Class: errs.ClassInternal}
	got := trustedReleaseArchiveFetchErrorContract{Code: domainErr.Code, Class: domainErr.Class}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("redirect rejection error mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, calls)
	assert.Equal(t, 1, body.closeCalls)
	assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
}

func TestTrustedReleaseArchiveFetcherRejectsResponseBeforeStageMutation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		status           int
		contentLength    string
		responseLength   int64
		transferEncoding []string
		headerValues     []string
	}{
		{name: "non-OK status", status: http.StatusNotFound, contentLength: "1", responseLength: 1},
		{name: "missing content length", status: http.StatusOK, responseLength: -1},
		{
			name:             "chunked body",
			status:           http.StatusOK,
			contentLength:    "1",
			responseLength:   -1,
			transferEncoding: []string{"chunked"},
		},
		{name: "zero length", status: http.StatusOK, contentLength: "0", responseLength: 0},
		{name: "negative length", status: http.StatusOK, contentLength: "-1", responseLength: -1},
		{name: "malformed length", status: http.StatusOK, contentLength: "not-a-number", responseLength: 1},
		{name: "comma-joined length", status: http.StatusOK, contentLength: "1, 1", responseLength: 1},
		{
			name:           "multiple effective lengths",
			status:         http.StatusOK,
			responseLength: 1,
			headerValues:   []string{"1", "1"},
		},
		{
			name:           "conflicting effective lengths",
			status:         http.StatusOK,
			responseLength: 1,
			headerValues:   []string{"1", "2"},
		},
		{
			name:           "length exceeds signed 63-bit range",
			status:         http.StatusOK,
			contentLength:  "9223372036854775808",
			responseLength: -1,
		},
		{
			name:           "length above policy",
			status:         http.StatusOK,
			contentLength:  "134217729",
			responseLength: int64(128*1024*1024 + 1),
		},
		{name: "length metadata mismatch", status: http.StatusOK, contentLength: "2", responseLength: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stage, _ := newTrustedReleaseArchiveStreamFixture(t)
			pwriteCalled := false
			stage.deps.pwrite = func(context.Context, int, []byte, int64) (int, error) {
				pwriteCalled = true
				return 0, errors.New("stage must not be written")
			}
			fsyncCalled := false
			stage.deps.fsync = func(context.Context, int) error {
				fsyncCalled = true
				return errors.New("stage must not be synced")
			}
			body := newTrackingArchiveBody(strings.NewReader("x"), nil)
			responseHeader := http.Header{}
			if tc.headerValues != nil {
				responseHeader["Content-Length"] = tc.headerValues
			} else if tc.contentLength != "" {
				responseHeader.Set("Content-Length", tc.contentLength)
			}
			fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
				func(request *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode:       tc.status,
						ContentLength:    tc.responseLength,
						TransferEncoding: tc.transferEncoding,
						Header:           responseHeader,
						Body:             body,
						Request:          request,
					}, nil
				},
			))

			err := fetcher.fetch(
				t.Context(),
				trustedReleaseArchiveFetchSource(t),
				stage,
				trustedReleaseArchiveDigest{},
			)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			want := trustedReleaseArchiveFetchErrorContract{
				Code:  errs.CodeDownloadFailed,
				Class: errs.ClassInternal,
			}
			got := trustedReleaseArchiveFetchErrorContract{Code: domainErr.Code, Class: domainErr.Class}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("response rejection error mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
			assert.False(t, pwriteCalled)
			assert.False(t, fsyncCalled)
			assert.Equal(t, 1, body.closeCalls)
		})
	}
}

func TestTrustedReleaseArchiveFetcherAdmitsExactLengthBoundaries(t *testing.T) {
	t.Parallel()

	t.Run("one byte", func(t *testing.T) {
		t.Parallel()

		payload := []byte("x")
		stage, _ := newTrustedReleaseArchiveStreamFixture(t)
		body := newTrackingArchiveBody(bytes.NewReader(payload), nil)
		fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				return trustedReleaseArchiveFetchResponse(request, http.StatusOK, "1", 1, nil, body), nil
			},
		))

		err := fetcher.fetch(
			t.Context(),
			trustedReleaseArchiveFetchSource(t),
			stage,
			trustedReleaseArchiveDigestForTest(payload),
		)
		require.NoError(t, err)
		assert.Equal(t, trustedReleaseArchiveStageReady, stage.state)
		assert.Equal(t, 1, body.closeCalls)
	})

	t.Run("maximum enters stage receive without allocation", func(t *testing.T) {
		t.Parallel()

		stage, _ := newTrustedReleaseArchiveStreamFixture(t)
		body := newTrackingArchiveBody(http.NoBody, nil)
		fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				return trustedReleaseArchiveFetchResponse(
					request,
					http.StatusOK,
					"134217728",
					int64(128*1024*1024),
					nil,
					body,
				), nil
			},
		))

		err := fetcher.fetch(
			t.Context(),
			trustedReleaseArchiveFetchSource(t),
			stage,
			trustedReleaseArchiveDigest{},
		)
		require.Error(t, err)
		assert.Equal(t, errs.CodeBinaryUntrusted, errs.AsDomainError(err).Code)
		assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
		assert.Equal(t, 1, body.closeCalls)
	})
}

func TestTrustedReleaseArchiveFetcherPoisonsStageAfterReceiveFailure(t *testing.T) {
	t.Parallel()

	payload := []byte("trusted archive")
	digest := trustedReleaseArchiveDigestForTest(payload)
	tests := []struct {
		name           string
		declaredLength int64
		body           func(context.CancelFunc) io.Reader
		digest         trustedReleaseArchiveDigest
		inject         func(*trustedReleaseArchiveStage)
		wantCode       errs.Code
		wantErr        error
	}{
		{
			name:           "short body",
			declaredLength: int64(len(payload) + 1),
			body:           func(context.CancelFunc) io.Reader { return bytes.NewReader(payload) },
			digest:         digest,
			wantCode:       errs.CodeBinaryUntrusted,
		},
		{
			name:           "long body",
			declaredLength: int64(len(payload)),
			body: func(context.CancelFunc) io.Reader {
				return bytes.NewReader(append(append([]byte(nil), payload...), '!'))
			},
			digest:   digest,
			wantCode: errs.CodeBinaryUntrusted,
		},
		{
			name:           "digest mismatch",
			declaredLength: int64(len(payload)),
			body:           func(context.CancelFunc) io.Reader { return bytes.NewReader(payload) },
			digest:         trustedReleaseArchiveDigest{},
			wantCode:       errs.CodeBinaryUntrusted,
		},
		{
			name:           "read failure",
			declaredLength: int64(len(payload)),
			body: func(context.CancelFunc) io.Reader {
				return io.MultiReader(
					bytes.NewReader(payload[:3]),
					trustedReleaseArchiveReaderFunc(func([]byte) (int, error) { return 0, unix.EIO }),
				)
			},
			digest:   digest,
			wantCode: errs.CodeBinaryTrustedInstallFailed,
			wantErr:  unix.EIO,
		},
		{
			name:           "cancellation during read",
			declaredLength: int64(len(payload)),
			body: func(cancel context.CancelFunc) io.Reader {
				return trustedReleaseArchiveReaderFunc(func(value []byte) (int, error) {
					count := copy(value, payload)
					cancel()
					return count, nil
				})
			},
			digest:   digest,
			wantCode: errs.CodeBinaryTrustedInstallFailed,
			wantErr:  context.Canceled,
		},
		{
			name:           "fsync failure",
			declaredLength: int64(len(payload)),
			body:           func(context.CancelFunc) io.Reader { return bytes.NewReader(payload) },
			digest:         digest,
			inject: func(stage *trustedReleaseArchiveStage) {
				stage.deps.fsync = func(context.Context, int) error { return unix.EIO }
			},
			wantCode: errs.CodeBinaryTrustedInstallFailed,
			wantErr:  unix.EIO,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stage, _ := newTrustedReleaseArchiveStreamFixture(t)
			if tc.inject != nil {
				tc.inject(stage)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			body := newTrackingArchiveBody(tc.body(cancel), nil)
			length := formatTrustedReleaseArchiveLength(tc.declaredLength)
			fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
				func(request *http.Request) (*http.Response, error) {
					return trustedReleaseArchiveFetchResponse(
						request,
						http.StatusOK,
						length,
						tc.declaredLength,
						nil,
						body,
					), nil
				},
			))

			err := fetcher.fetch(ctx, trustedReleaseArchiveFetchSource(t), stage, tc.digest)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, tc.wantCode, domainErr.Code)
			assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
			assert.Equal(t, 1, body.closeCalls)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			}

			retryCalls := 0
			retryFetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
				func(*http.Request) (*http.Response, error) {
					retryCalls++
					return nil, errors.New("poisoned stage must reject before request")
				},
			))
			retryErr := retryFetcher.fetch(
				t.Context(),
				trustedReleaseArchiveFetchSource(t),
				stage,
				trustedReleaseArchiveDigest{},
			)
			require.Error(t, retryErr)
			assert.Equal(t, errs.CodeBinaryTrustedInstallFailed, errs.AsDomainError(retryErr).Code)
			assert.Equal(t, 0, retryCalls)
		})
	}
}

func TestTrustedReleaseArchiveFetcherDoesNotRetryTransportFailure(t *testing.T) {
	t.Parallel()

	calls := 0
	transportErr := errors.New("network unavailable")
	fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
		func(*http.Request) (*http.Response, error) {
			calls++
			return nil, transportErr
		},
	))
	stage, _ := newTrustedReleaseArchiveStreamFixture(t)

	err := fetcher.fetch(t.Context(), trustedReleaseArchiveFetchSource(t), stage, trustedReleaseArchiveDigest{})
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeDownloadFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.ErrorIs(t, err, transportErr)
	assert.Equal(t, 1, calls)
	assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
}

func TestTrustedReleaseArchiveFetcherPreservesClientTimeout(t *testing.T) {
	t.Parallel()

	calls := 0
	fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			calls++
			<-request.Context().Done()
			return nil, request.Context().Err()
		},
	))
	fetcher.client.Timeout = 20 * time.Millisecond
	stage, _ := newTrustedReleaseArchiveStreamFixture(t)

	err := fetcher.fetch(t.Context(), trustedReleaseArchiveFetchSource(t), stage, trustedReleaseArchiveDigest{})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, errs.CodeDownloadFailed, errs.AsDomainError(err).Code)
	assert.Equal(t, 1, calls)
	assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
}

func TestTrustedReleaseArchiveFetcherPreservesInFlightCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	calls := 0
	fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			calls++
			close(started)
			<-request.Context().Done()
			return nil, request.Context().Err()
		},
	))
	stage, _ := newTrustedReleaseArchiveStreamFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- fetcher.fetch(ctx, trustedReleaseArchiveFetchSource(t), stage, trustedReleaseArchiveDigest{})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("archive request did not enter the transport")
	}
	cancel()

	select {
	case err := <-result:
		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, errs.CodeDownloadFailed, errs.AsDomainError(err).Code)
	case <-time.After(time.Second):
		t.Fatal("archive request did not stop after cancellation")
	}
	assert.Equal(t, 1, calls)
	assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
}

func TestTrustedReleaseArchiveFetcherHandlesResponseCloseFailures(t *testing.T) {
	t.Parallel()

	t.Run("successful receive is poisoned", func(t *testing.T) {
		t.Parallel()

		payload := []byte("x")
		closeErr := errors.New("close failed")
		body := newTrackingArchiveBody(bytes.NewReader(payload), closeErr)
		stage, _ := newTrustedReleaseArchiveStreamFixture(t)
		fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				return trustedReleaseArchiveFetchResponse(request, http.StatusOK, "1", 1, nil, body), nil
			},
		))

		err := fetcher.fetch(
			t.Context(),
			trustedReleaseArchiveFetchSource(t),
			stage,
			trustedReleaseArchiveDigestForTest(payload),
		)
		require.Error(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		want := trustedReleaseArchiveFetchErrorContract{Code: errs.CodeDownloadFailed, Class: errs.ClassInternal}
		got := trustedReleaseArchiveFetchErrorContract{Code: domainErr.Code, Class: domainErr.Class}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("successful-receive close error mismatch (-want +got):\n%s", diff)
		}
		assert.ErrorIs(t, err, closeErr)
		assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
		assert.Equal(t, uint64(0), stage.sizeBytes)
		if diff := cmp.Diff(trustedReleaseArchiveDigest{}, stage.archiveDigest); diff != "" {
			t.Errorf("poisoned stage digest mismatch (-want +got):\n%s", diff)
		}
		assert.Equal(t, 1, body.closeCalls)
	})

	t.Run("receive failure remains primary", func(t *testing.T) {
		t.Parallel()

		readErr := errors.New("read failed")
		closeErr := errors.New("close failed")
		body := newTrackingArchiveBody(trustedReleaseArchiveReaderFunc(
			func([]byte) (int, error) { return 0, readErr },
		), closeErr)
		stage, _ := newTrustedReleaseArchiveStreamFixture(t)
		fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				return trustedReleaseArchiveFetchResponse(request, http.StatusOK, "1", 1, nil, body), nil
			},
		))

		err := fetcher.fetch(t.Context(), trustedReleaseArchiveFetchSource(t), stage, trustedReleaseArchiveDigest{})
		require.Error(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		want := trustedReleaseArchiveFetchErrorContract{
			Code:  errs.CodeBinaryTrustedInstallFailed,
			Class: errs.ClassInternal,
		}
		got := trustedReleaseArchiveFetchErrorContract{Code: domainErr.Code, Class: domainErr.Class}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("receive-close error contract mismatch (-want +got):\n%s", diff)
		}
		assert.ErrorIs(t, err, readErr)
		assert.ErrorIs(t, err, closeErr)
		assert.Equal(t, trustedReleaseArchiveStageFailed, stage.state)
		assert.Equal(t, 1, body.closeCalls)
	})

	t.Run("pre-admission rejection remains primary and stage stays empty", func(t *testing.T) {
		t.Parallel()

		closeErr := errors.New("close failed")
		body := newTrackingArchiveBody(strings.NewReader("missing"), closeErr)
		stage, _ := newTrustedReleaseArchiveStreamFixture(t)
		fetcher := testTrustedReleaseArchiveFetcher(trustedReleaseRoundTripFunc(
			func(request *http.Request) (*http.Response, error) {
				return trustedReleaseArchiveFetchResponse(
					request,
					http.StatusNotFound,
					"7",
					7,
					nil,
					body,
				), nil
			},
		))

		err := fetcher.fetch(t.Context(), trustedReleaseArchiveFetchSource(t), stage, trustedReleaseArchiveDigest{})
		require.Error(t, err)
		domainErr := errs.AsDomainError(err)
		require.NotNil(t, domainErr)
		assert.Equal(t, errs.CodeDownloadFailed, domainErr.Code)
		assert.Equal(t, errs.ClassInternal, domainErr.Class)
		assert.ErrorIs(t, err, closeErr)
		assert.Equal(t, trustedReleaseArchiveStageEmpty, stage.state)
		assert.Equal(t, 1, body.closeCalls)
	})
}

// Rationale: Cleanup diagnostics must enrich the exact primary DomainError rather than replacing its typed metadata.
func TestAppendTrustedReleaseArchiveCloseErrorPreservesPrimaryDomainError(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary cause")
	closeErr := errors.New("close cause")
	primary := errs.WrapMsg(
		errs.CodeBinaryUntrusted,
		"primary message",
		primaryCause,
		errs.WithClass(errs.ClassValidation),
		errs.WithEntity("release-identity"),
		errs.WithDetails(map[string]any{"accepted": false}),
	)
	wantDetails := map[string]any{"accepted": false}

	got := appendTrustedReleaseArchiveCloseError(primary, closeErr)
	domainErr := errs.AsDomainError(got)
	require.NotNil(t, domainErr)
	assert.Same(t, primary, domainErr)
	want := trustedReleaseArchiveFetchErrorMetadata{
		Code:    errs.CodeBinaryUntrusted,
		Class:   errs.ClassValidation,
		Op:      primary.Op,
		Entity:  "release-identity",
		Details: wantDetails,
	}
	actual := trustedReleaseArchiveFetchErrorMetadata{
		Code:    domainErr.Code,
		Class:   domainErr.Class,
		Op:      domainErr.Op,
		Entity:  domainErr.Entity,
		Details: domainErr.Details,
	}
	if diff := cmp.Diff(want, actual); diff != "" {
		t.Errorf("close error metadata mismatch (-want +got):\n%s", diff)
	}
	assert.ErrorIs(t, got, primaryCause)
	assert.ErrorIs(t, got, closeErr)
	assert.Contains(t, domainErr.Message, "close trusted release archive response")
}

type trustedReleaseArchiveFetchErrorContract struct {
	Code  errs.Code
	Class errs.Class
}

type trustedReleaseArchiveFetchErrorMetadata struct {
	Code    errs.Code
	Class   errs.Class
	Op      string
	Entity  string
	Details map[string]any
}

type trackingArchiveBody struct {
	reader     io.Reader
	closeErr   error
	closeCalls int
}

func newTrackingArchiveBody(reader io.Reader, closeErr error) *trackingArchiveBody {
	return &trackingArchiveBody{reader: reader, closeErr: closeErr}
}

func (body *trackingArchiveBody) Read(value []byte) (int, error) {
	return body.reader.Read(value)
}

func (body *trackingArchiveBody) Close() error {
	body.closeCalls++
	return body.closeErr
}

func trustedReleaseArchiveFetchSource(t *testing.T) trustedReleaseSource {
	t.Helper()

	source, err := newTrustedReleaseSource(releaseSlot{version: "1.16.1", architecture: "x86_64"})
	require.NoError(t, err)
	return source
}

func testTrustedReleaseArchiveFetcher(roundTripper http.RoundTripper) *trustedReleaseArchiveFetcher {
	return &trustedReleaseArchiveFetcher{client: &http.Client{
		Transport:     roundTripper,
		CheckRedirect: checkTrustedReleaseArchiveRedirect,
		Timeout:       2 * time.Second,
	}}
}

func trustedReleaseArchiveFetchResponse(
	request *http.Request,
	status int,
	contentLength string,
	responseLength int64,
	transferEncoding []string,
	body io.ReadCloser,
) *http.Response {
	header := http.Header{}
	if contentLength != "" {
		header.Set("Content-Length", contentLength)
	}
	return &http.Response{
		StatusCode:       status,
		ContentLength:    responseLength,
		TransferEncoding: transferEncoding,
		Header:           header,
		Body:             body,
		Request:          request,
	}
}

func cloneTrustedReleaseArchiveRequest(t *testing.T, request *http.Request) *http.Request {
	t.Helper()

	clone := request.Clone(t.Context())
	clone.URL = &url.URL{}
	*clone.URL = *request.URL
	return clone
}

func newTrustedReleaseArchiveRequestForTest(t *testing.T, rawURL string) *http.Request {
	t.Helper()

	request, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	return request
}

func formatTrustedReleaseArchiveLength(length int64) string {
	return strconv.FormatInt(length, 10)
}
