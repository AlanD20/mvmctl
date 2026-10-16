package jailer

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"mvmctl/pkg/errs"
)

const (
	trustedReleaseChecksumMaxBytes       = 256
	trustedReleaseChecksumMaxHeaderBytes = 16 * 1024
	trustedReleaseChecksumPhaseTimeout   = 5 * time.Second
	trustedReleaseChecksumTotalTimeout   = 15 * time.Second
	trustedReleaseChecksumAssetHost      = "release-assets.githubusercontent.com"
	trustedReleaseChecksumAccept         = "text/plain"
	trustedReleaseChecksumUserAgent      = "mvmctl-trusted-release/1"
)

type trustedReleaseArchiveDigest [sha256.Size]byte

type trustedReleaseChecksumAuthority struct {
	client *http.Client
}

func newTrustedReleaseChecksumAuthority() *trustedReleaseChecksumAuthority {
	dialer := &net.Dialer{
		Timeout:   trustedReleaseChecksumPhaseTimeout,
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
		TLSHandshakeTimeout:    trustedReleaseChecksumPhaseTimeout,
		ResponseHeaderTimeout:  trustedReleaseChecksumPhaseTimeout,
		MaxResponseHeaderBytes: trustedReleaseChecksumMaxHeaderBytes,
	}
	return &trustedReleaseChecksumAuthority{client: &http.Client{
		Transport:     transport,
		CheckRedirect: checkTrustedReleaseChecksumRedirect,
		Timeout:       trustedReleaseChecksumTotalTimeout,
	}}
}

// CRITICAL: The request target is reconstructed and verified before this method performs a network effect. The
// dedicated client has no proxy, cache, decompression, or retry path through which ambient user state can gain authority.
func (authority *trustedReleaseChecksumAuthority) fetch(
	ctx context.Context,
	source trustedReleaseSource,
) (digest trustedReleaseArchiveDigest, returnErr error) {
	if err := validateTrustedReleaseSource(source); err != nil {
		return trustedReleaseArchiveDigest{}, err
	}
	if err := ctx.Err(); err != nil {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumDownloadError(
			"fetch trusted release checksum",
			err,
		)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.checksumURL, nil)
	if err != nil {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumDownloadError(
			"create trusted release checksum request",
			err,
		)
	}
	request.Header.Set("Accept", trustedReleaseChecksumAccept)
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", trustedReleaseChecksumUserAgent)

	response, err := authority.client.Do(request)
	if err != nil {
		// http.Client closes any non-nil response body returned with a redirect-policy error.
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumDownloadError(
			"fetch trusted release checksum",
			err,
		)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			returnErr = appendTrustedReleaseChecksumCloseError(returnErr, closeErr)
		}
	}()

	if response.StatusCode != http.StatusOK {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumDownloadError(
			fmt.Sprintf("trusted release checksum returned HTTP status %d", response.StatusCode),
			nil,
		)
	}
	if response.ContentLength < -1 || response.ContentLength > trustedReleaseChecksumMaxBytes {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumDownloadError(
			"trusted release checksum response exceeds size limit",
			nil,
		)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, trustedReleaseChecksumMaxBytes+1))
	if err != nil {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumDownloadError(
			"read trusted release checksum response",
			err,
		)
	}
	if len(raw) > trustedReleaseChecksumMaxBytes {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumDownloadError(
			"trusted release checksum response exceeds size limit",
			nil,
		)
	}

	return parseTrustedReleaseChecksum(raw, source)
}

func checkTrustedReleaseChecksumRedirect(request *http.Request, via []*http.Request) error {
	if len(via) != 1 || request.Method != http.MethodGet || request.URL.Scheme != "https" ||
		request.URL.Host != trustedReleaseChecksumAssetHost || request.URL.User != nil ||
		request.URL.Fragment != "" || request.URL.RawFragment != "" {
		return errs.New(
			errs.CodeBinaryUntrusted,
			"trusted release checksum redirect is not permitted",
		)
	}
	return nil
}

func parseTrustedReleaseChecksum(
	raw []byte,
	source trustedReleaseSource,
) (trustedReleaseArchiveDigest, error) {
	if err := validateTrustedReleaseSource(source); err != nil {
		return trustedReleaseArchiveDigest{}, err
	}
	expectedLength := sha256.Size*2 + 2 + len(source.archiveName) + 1
	if len(raw) > trustedReleaseChecksumMaxBytes || len(raw) != expectedLength {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumUntrustedError(
			"trusted release checksum sidecar has invalid length",
			nil,
		)
	}
	digestEnd := sha256.Size * 2
	if !authorityHashPattern.Match(raw[:digestEnd]) {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumUntrustedError(
			"trusted release checksum digest is not lowercase SHA-256",
			nil,
		)
	}
	if string(raw[digestEnd:digestEnd+2]) != "  " {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumUntrustedError(
			"trusted release checksum separator is invalid",
			nil,
		)
	}
	if string(raw[digestEnd+2:len(raw)-1]) != source.archiveName || raw[len(raw)-1] != '\n' {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumUntrustedError(
			"trusted release checksum archive identity is invalid",
			nil,
		)
	}

	var digest trustedReleaseArchiveDigest
	if _, err := hex.Decode(digest[:], raw[:digestEnd]); err != nil {
		return trustedReleaseArchiveDigest{}, trustedReleaseChecksumUntrustedError(
			"decode trusted release checksum digest",
			err,
		)
	}
	return digest, nil
}

func trustedReleaseChecksumDownloadError(message string, cause error) *errs.DomainError {
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

func trustedReleaseChecksumUntrustedError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeBinaryUntrusted, message)
	}
	return errs.WrapMsg(errs.CodeBinaryUntrusted, message, cause, errs.WithClass(errs.ClassValidation))
}

func appendTrustedReleaseChecksumCloseError(primary, cause error) *errs.DomainError {
	const message = "close trusted release checksum response"
	if primary == nil {
		return trustedReleaseChecksumDownloadError(message, cause)
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		return trustedReleaseChecksumDownloadError(
			primary.Error()+"; "+message+": "+cause.Error(),
			errors.Join(primary, cause),
		)
	}
	domainErr.Message += "; " + message + ": " + cause.Error()
	domainErr.Err = errors.Join(domainErr.Err, cause)
	return domainErr
}
