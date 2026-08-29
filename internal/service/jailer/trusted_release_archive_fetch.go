package jailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"mvmctl/pkg/errs"
)

const (
	trustedReleaseArchiveFetchMaxHeaderBytes = 16 * 1024
	trustedReleaseArchiveFetchPhaseTimeout   = 5 * time.Second
	trustedReleaseArchiveFetchTotalTimeout   = 5 * time.Minute
	trustedReleaseArchiveFetchAssetHost      = "release-assets.githubusercontent.com"
	trustedReleaseArchiveFetchAccept         = "application/octet-stream"
	trustedReleaseArchiveFetchUserAgent      = "mvmctl-trusted-release/1"
)

type trustedReleaseArchiveFetcher struct {
	client *http.Client
}

func newTrustedReleaseArchiveFetcher() *trustedReleaseArchiveFetcher {
	dialer := &net.Dialer{
		Timeout:   trustedReleaseArchiveFetchPhaseTimeout,
		KeepAlive: -1,
	}
	transport := &http.Transport{
		Proxy:       nil,
		DialContext: dialer.DialContext,
		// CRITICAL: Go's HTTP/2 transport internally retries some bodyless requests. HTTP/1 preserves the no-retry rule.
		ForceAttemptHTTP2:      false,
		MaxConnsPerHost:        1,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    trustedReleaseArchiveFetchPhaseTimeout,
		ResponseHeaderTimeout:  trustedReleaseArchiveFetchPhaseTimeout,
		MaxResponseHeaderBytes: trustedReleaseArchiveFetchMaxHeaderBytes,
	}
	return &trustedReleaseArchiveFetcher{client: &http.Client{
		Transport:     transport,
		CheckRedirect: checkTrustedReleaseArchiveRedirect,
		Timeout:       trustedReleaseArchiveFetchTotalTimeout,
	}}
}

// CRITICAL: The request target is receiver-derived and revalidated before the network effect. Redirects are limited
// to GitHub's exact release-asset host, and the response reaches only an anonymous root-owned stage after admission.
func (fetcher *trustedReleaseArchiveFetcher) fetch(
	ctx context.Context,
	source trustedReleaseSource,
	stage *trustedReleaseArchiveStage,
	expectedDigest trustedReleaseArchiveDigest,
) (returnErr error) {
	if err := validateTrustedReleaseSource(source); err != nil {
		return err
	}
	if err := stage.requireEmptyForReceive(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseArchiveDownloadError("fetch trusted release archive", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.archiveURL, nil)
	if err != nil {
		return trustedReleaseArchiveDownloadError("create trusted release archive request", err)
	}
	setTrustedReleaseArchiveRequestHeaders(request)

	response, err := fetcher.client.Do(request)
	if err != nil {
		// http.Client closes any non-nil response body returned with a redirect-policy error.
		return trustedReleaseArchiveDownloadError("fetch trusted release archive", err)
	}
	received := false
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			if received {
				stage.poisonReadyAfterReceive()
			}
			returnErr = appendTrustedReleaseArchiveCloseError(returnErr, closeErr)
		}
	}()

	if response.StatusCode != http.StatusOK {
		return trustedReleaseArchiveDownloadError(
			fmt.Sprintf("trusted release archive returned HTTP status %d", response.StatusCode),
			nil,
		)
	}
	declaredBytes, err := admittedTrustedReleaseArchiveResponseLength(response)
	if err != nil {
		return err
	}
	if err := stage.receive(ctx, response.Body, declaredBytes, expectedDigest); err != nil {
		return err
	}
	received = true
	return nil
}

func setTrustedReleaseArchiveRequestHeaders(request *http.Request) {
	request.Header = http.Header{
		"Accept":          []string{trustedReleaseArchiveFetchAccept},
		"Accept-Encoding": []string{"identity"},
		"Cache-Control":   []string{"no-store"},
		"User-Agent":      []string{trustedReleaseArchiveFetchUserAgent},
	}
	request.Host = ""
}

func checkTrustedReleaseArchiveRedirect(request *http.Request, via []*http.Request) error {
	if len(via) != 1 || request == nil || request.URL == nil || request.Method != http.MethodGet ||
		request.URL.Scheme != "https" || request.URL.Host != trustedReleaseArchiveFetchAssetHost ||
		request.URL.User != nil || request.URL.Fragment != "" || request.URL.RawFragment != "" ||
		request.URL.Opaque != "" {
		return trustedReleaseArchiveDownloadError("trusted release archive redirect is not permitted", nil)
	}
	setTrustedReleaseArchiveRequestHeaders(request)
	return nil
}

func admittedTrustedReleaseArchiveResponseLength(response *http.Response) (uint64, error) {
	if len(response.TransferEncoding) != 0 {
		return 0, trustedReleaseArchiveDownloadError(
			"trusted release archive response uses transfer encoding",
			nil,
		)
	}
	values := response.Header.Values("Content-Length")
	if len(values) != 1 {
		return 0, trustedReleaseArchiveDownloadError(
			"trusted release archive response must declare one content length",
			nil,
		)
	}
	declaredBytes, err := strconv.ParseUint(values[0], 10, 63)
	if err != nil {
		return 0, trustedReleaseArchiveDownloadError(
			"trusted release archive response content length is invalid",
			err,
		)
	}
	if declaredBytes == 0 || declaredBytes > trustedReleaseArchiveMaxBytes ||
		response.ContentLength < 0 || uint64(response.ContentLength) != declaredBytes {
		return 0, trustedReleaseArchiveDownloadError(
			"trusted release archive response content length is outside policy or inconsistent",
			nil,
		)
	}
	return declaredBytes, nil
}

func trustedReleaseArchiveDownloadError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeDownloadFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(
		errs.CodeDownloadFailed,
		message,
		cause,
		errs.WithClass(errs.ClassInternal),
	)
}

func appendTrustedReleaseArchiveCloseError(primary, cause error) error {
	const message = "close trusted release archive response"
	if primary == nil {
		return trustedReleaseArchiveDownloadError(message, cause)
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + message + ": " + cause.Error()
		domainErr.Err = errors.Join(domainErr.Err, cause)
		return domainErr
	}
	return trustedReleaseArchiveDownloadError(
		primary.Error()+"; "+message+": "+cause.Error(),
		errors.Join(primary, cause),
	)
}
