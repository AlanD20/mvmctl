package image

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/download"
	"mvmctl/pkg/errs"
)

const sha256DigestSize = sha256.Size

type imageChecksum struct {
	sha256 string
	sha512 string
}

func (s *Service) fetchImageChecksum(ctx context.Context, checksumURL, sourceFilename string) (imageChecksum, error) {
	parsedURL, err := url.Parse(checksumURL)
	if err != nil {
		return imageChecksum{}, errs.WrapMsg(errs.CodeDownloadFailed, "invalid image checksum URL", err)
	}
	// NOTE: The existing SHA256URL catalog field also carries Alpine's SHA512 sidecar URL.
	isSHA512 := strings.EqualFold(filepath.Ext(parsedURL.Path), ".sha512")
	digestSize := sha256.Size
	if isSHA512 {
		digestSize = sha512.Size
	}
	content, err := s.dl.GetContent(ctx, download.RequestOpts{
		URL: checksumURL, Timeout: infra.HTTPTimeoutSha256FetchS,
	})
	if err != nil {
		return imageChecksum{}, errs.WrapMsg(errs.CodeDownloadFailed, "failed to fetch image checksum", err)
	}
	digest, err := parseImageChecksum(content, sourceFilename, digestSize)
	if err != nil {
		return imageChecksum{}, err
	}
	if isSHA512 {
		return imageChecksum{sha512: digest}, nil
	}
	return imageChecksum{sha256: digest}, nil
}

func parseImageChecksum(content, sourceFilename string, digestSize int) (string, error) {
	content = strings.TrimSpace(content)
	fields := strings.Fields(content)
	if len(fields) == 1 {
		return validateImageDigest(fields[0], digestSize)
	}
	var digest string
	for line := range strings.SplitSeq(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 2 {
			return "", errs.New(
				errs.CodeDownloadFailed,
				"invalid image checksum metadata: expected digest and filename",
			)
		}
		rowDigest, err := validateImageDigest(fields[0], digestSize)
		if err != nil {
			return "", err
		}
		filename := strings.TrimPrefix(fields[1], "*")
		if filepath.Base(filename) != filepath.Base(sourceFilename) {
			continue
		}
		if digest != "" {
			return "", errs.New(
				errs.CodeDownloadFailed,
				"ambiguous image checksum metadata: multiple matching filenames",
			)
		}
		digest = rowDigest
	}
	if digest == "" {
		return "", errs.New(errs.CodeDownloadFailed, "image checksum metadata contains no digest for the source file")
	}
	return digest, nil
}

func validateImageDigest(digest string, digestSize int) (string, error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != digestSize {
		return "", errs.New(errs.CodeDownloadFailed, "invalid image checksum: expected a complete hexadecimal digest")
	}
	return strings.ToLower(digest), nil
}
