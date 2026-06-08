package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/pkg/errs"
)

// RequestOpts bundles common HTTP request parameters.
// Used by GetContent, HeadSize, GetJSON, and DownloadFile.
type RequestOpts struct {
	URL             string
	Timeout         int
	Headers         map[string]string
	UseCache        bool
	CacheTTLSeconds int
}

// ── HttpDiskCache (file-based HTTP response cache) ──────────────────────────

// DefaultCacheTTLSeconds is the default TTL for cached HTTP responses (300s).
// Mirrors Python's DEFAULT_CACHE_TTL_SECONDS: int = 300.
const DefaultCacheTTLSeconds = 300

// DefaultCacheDir is the subdirectory within the temp dir for HTTP cache files.
// Mirrors Python's DEFAULT_CACHE_DIR = "http".
const DefaultCacheDir = "http"

// HttpDiskCache provides file-based caching for small remote HTTP resources.
// Mirrors Python's HttpCache class in utils/http.py.
//
// Python uses all @staticmethod methods with no instance state. Go mirrors
// this with a stateless struct and separate functions.
type HttpDiskCache struct{}

// NewHttpDiskCache creates a new HttpDiskCache. No state is maintained.
func NewHttpDiskCache() *HttpDiskCache {
	return &HttpDiskCache{}
}

// key returns the SHA256 hex digest of the URL for use as a cache key.
func (c *HttpDiskCache) key(url string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:])
}

// Path returns the filesystem path for a cached URL.
func (c *HttpDiskCache) Path(url string) string {
	cacheDir := filepath.Join(infra.GetTempDir(), DefaultCacheDir)
	return filepath.Join(cacheDir, c.key(url))
}

// IsValid returns true if the cached file exists and is younger than ttlSeconds.
func (c *HttpDiskCache) IsValid(cachePath string, ttlSeconds int) bool {
	info, err := os.Stat(cachePath)
	if err != nil {
		return false
	}
	age := time.Since(info.ModTime())
	return age < time.Duration(ttlSeconds)*time.Second
}

// Read reads the full contents of a cached file.
func (c *HttpDiskCache) Read(cachePath string) ([]byte, error) {
	return os.ReadFile(cachePath)
}

// Write atomically writes data to the cache using tempfile + rename.
func (c *HttpDiskCache) Write(data []byte, cachePath string) error {
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, infra.DirPerm); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	stem := strings.TrimSuffix(filepath.Base(cachePath), filepath.Ext(cachePath))
	tmpFile, err := os.CreateTemp(dir, stem+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write cache: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close cache temp: %w", err)
	}
	if err := os.Rename(tmpPath, cachePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename cache: %w", err)
	}
	return nil
}

// ── User-Agent ────────────────────────────────────────────────────────────

// UserAgent is the HTTP User-Agent header value.
// Defaults to "mvmctl/dev". Set via SetUserAgent at startup.
// Mirrors Python's HTTP_USER_AGENT constant.
var UserAgent = infra.DefaultUserAgent

// SetUserAgent sets the User-Agent header value.
// Called at startup by the application to set the dynamic version.
func SetUserAgent(version string) {
	UserAgent = fmt.Sprintf("%s/%s", infra.CLIName, version)
}

// HttpError represents an HTTP-level error (matching Python's URLError/HTTPError).
type HttpError struct {
	StatusCode int
	URL        string
}

func (e HttpError) Error() string {
	return fmt.Sprintf("HTTP %d for %s", e.StatusCode, e.URL)
}

// ProgressFunc is called per-chunk during download with the raw chunk bytes.
// Mirrors Python's Callable[[bytes], None].
type ProgressFunc func(chunk []byte)

// ── Downloader ─────────────────────────────────────────────────────────────

// Downloader handles HTTP downloads with retry, mirror support, and checksum
// verification. Mirrors Python's HttpDownload class in utils/http.py.
//
// Python uses all @staticmethod methods with shared module-level opener.
// Go uses a struct to bundle configuration, matching the same semantics.
type Downloader struct {
	client  *http.Client
	retries int
	delay   time.Duration
	backoff float64
	cache   *HttpDiskCache

	// ConfirmFn is an optional callback for user-facing confirmation prompts.
	// When set, it is called instead of directly interacting with the terminal.
	// The string argument is the prompt message. Return true to proceed, false
	// to cancel. When nil (default), the operation auto-confirms with a warning log.
	// This enables the CLI layer to handle user interaction without the infra
	// layer writing to stderr/stdin directly.
	ConfirmFn func(prompt string) bool
}

// New creates a new Downloader with default settings.
func New() *Downloader {
	return &Downloader{
		client: &http.Client{
			Timeout: infra.HTTPTimeout,
		},
		retries: infra.HTTPMaxRetries,
		delay:   infra.HTTPRetryDelay,
		backoff: infra.HTTPBackoffFactor,
		cache:   NewHttpDiskCache(),
	}
}

// WithCache sets a custom HttpDiskCache on the Downloader.
func (d *Downloader) WithCache(c *HttpDiskCache) *Downloader {
	d.cache = c
	return d
}

// newRequest creates an HTTP request with the dynamic User-Agent header.
func (d *Downloader) newRequest(ctx context.Context, method, urlStr string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	return req, nil
}

// ── Pure transport: with_download (no checksum) ───────────────────────────

// WithDownload downloads a remote file to dest with optional progress callback.
// This is the **pure transport** entry point: it handles only HTTP mechanics,
// retries, and atomic placement. No checksum logic or progress-bar rendering
// lives here. Mirrors Python's HttpDownload.with_download() exactly.
//
// The file is downloaded to a temporary sibling of dest and then atomically
// promoted with os.Rename, so readers never see a partially-written file.
//
// Returns the total Content-Length if the server reported one, else -1
// (matching Python's int | None — -1 represents None).
func (d *Downloader) WithDownload(
	ctx context.Context,
	url, dest string,
	onProgress ProgressFunc,
	onStart func(totalSize int64),
) (totalSize int64, err error) {
	return d.withDownloadWithRetry(ctx, url, dest, onProgress, onStart)
}

// withDownloadOnce performs a single HTTP download attempt (no retry).
func (d *Downloader) withDownloadOnce(
	ctx context.Context,
	url, dest string,
	onProgress ProgressFunc,
	onStart func(totalSize int64),
) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), infra.DirPerm); err != nil {
		return 0, fmt.Errorf("create dest dir: %w", err)
	}

	req, err := d.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, &urlpkg.Error{Op: "GET", URL: url, Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, HttpError{StatusCode: resp.StatusCode, URL: url}
	}

	totalSize := resp.ContentLength

	// Create temp file for atomic write
	tmpFile, err := os.CreateTemp(filepath.Dir(dest), "*.tmp")
	if err != nil {
		if pathErr, ok := err.(*os.PathError); ok && pathErr.Err == syscall.EDQUOT {
			return 0, fmt.Errorf(
				"No storage available: insufficient space in /tmp. " +
					"Clear temporary files or increase disk space to continue.")
		}
		return 0, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if onStart != nil {
		onStart(totalSize)
	}

	buf := make([]byte, infra.HTTPChunkSize)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, writeErr := tmpFile.Write(chunk); writeErr != nil {
				tmpFile.Close()
				os.Remove(tmpPath)
				if pathErr, ok := writeErr.(*os.PathError); ok && pathErr.Err == syscall.EDQUOT {
					return 0, fmt.Errorf(
						"No storage available: insufficient space in /tmp. " +
							"Clear temporary files or increase disk space to continue.")
				}
				return 0, fmt.Errorf("write file: %w", writeErr)
			}
			if onProgress != nil {
				onProgress(chunk)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return 0, fmt.Errorf("read body: %w", readErr)
		}
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		os.Remove(tmpPath)
		return 0, fmt.Errorf("rename temp file: %w", err)
	}

	return totalSize, nil
}

// isRetryableError checks if an error corresponds to Python's retryable
// exceptions tuple: (URLError, HTTPError, IOError).
func isRetryableError(err error) bool {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return false
	}

	var urlErr *urlpkg.Error
	if errors.As(err, &urlErr) {
		return true
	}

	var httpErr HttpError
	if errors.As(err, &httpErr) {
		return true
	}

	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return true
	}

	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return true
	}

	return false
}

// withDownloadWithRetry implements the retry loop matching Python's @_with_retry decorator.
// Retryable exceptions: (URLError, HTTPError, IOError).
func (d *Downloader) withDownloadWithRetry(
	ctx context.Context,
	url, dest string,
	onProgress ProgressFunc,
	onStart func(totalSize int64),
) (int64, error) {
	var lastErr error
	delay := d.delay

	for attempt := 0; attempt <= d.retries; attempt++ {
		totalSize, err := d.withDownloadOnce(ctx, url, dest, onProgress, onStart)
		if err == nil {
			return totalSize, nil
		}

		lastErr = err

		if !isRetryableError(err) {
			return totalSize, err
		}

		if attempt < d.retries {
			slog.Warn("Download failed, retrying",
				"attempt", attempt+1,
				"max_attempts", d.retries+1,
				"error", err,
				"delay_seconds", delay.Seconds(),
			)
			time.Sleep(delay)
			delay = time.Duration(float64(delay) * d.backoff)
		} else {
			slog.Error("Download failed after all attempts",
				"attempts", d.retries+1,
				"error", err,
			)
		}
	}

	return -1, fmt.Errorf("download failed after %d retries: %w", d.retries, lastErr)
}

// ── Resolve mirror path (MVM_ASSET_MIRROR) ────────────────────────────────

// resolveMirrorPath checks if the URL's file exists in the local asset mirror.
// Reads MVM_ASSET_MIRROR env var. Mirrors Python's HttpDownload._resolve_mirror_path().
func resolveMirrorPath(rawURL string) (string, bool) {
	mirrorDir, ok := infra.EnvGet("ASSET_MIRROR")
	if !ok || mirrorDir == "" {
		return "", false
	}
	filename := extractFilename(rawURL)
	mirrorPath := filepath.Join(mirrorDir, filename)
	info, err := os.Stat(mirrorPath)
	if err == nil && !info.IsDir() {
		return mirrorPath, true
	}
	return "", false
}

// extractFilename extracts the filename from a URL (last segment, strip query params).
// Mirrors Python: url.rsplit("/", 1)[-1].split("?", 1)[0]
func extractFilename(rawURL string) string {
	if idx := strings.LastIndex(rawURL, "/"); idx >= 0 {
		rawURL = rawURL[idx+1:]
	}
	if before, _, found := strings.Cut(rawURL, "?"); found {
		rawURL = before
	}
	return rawURL
}

// sha256File computes the SHA256 hex digest of a file.
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── Orchestration: download_file ──────────────────────────────────────────

// DownloadFile downloads a file with optional SHA256 verification, mirror
// support, and missing-checksum logic. This is the orchestration entry point:
// it delegates HTTP transfer to WithDownload and handles checksum verification
// and user interaction for missing checksums.
//
// Mirrors Python's HttpDownload.download_file() exactly.
func (d *Downloader) DownloadFile(
	ctx context.Context,
	url, dest, expectedSHA256 string,
	allowMissingChecksum, silentMissingChecksum bool,
	progress event.OnDownloadCallback,
) error {
	if err := os.MkdirAll(filepath.Dir(dest), infra.DirPerm); err != nil {
		return fmt.Errorf("create dest dir: %w", err)
	}

	// ── Local asset mirror check ──
	mirrorPath, found := resolveMirrorPath(url)
	if found {
		slog.Info("Using local mirror for download", "url", url)
		if err := infra.CopyPreservingMetadata(mirrorPath, dest); err != nil {
			return fmt.Errorf("copy from mirror: %w", err)
		}
		if expectedSHA256 != "" {
			actualHash, err := sha256File(dest)
			if err != nil {
				return fmt.Errorf("sha256 mirror file: %w", err)
			}
			if strings.EqualFold(actualHash, expectedSHA256) {
				return nil
			}
			slog.Warn("Mirror checksum mismatch, falling back to HTTP download", "url", url)
			os.Remove(dest)
			os.Remove(mirrorPath) // Remove stale mirror so autoPopulateMirror can replace it
		} else {
			return nil
		}
	}

	// ── Handle missing checksum ──
	if expectedSHA256 == "" {
		if silentMissingChecksum {
			// pass — no warnings
		} else if !allowMissingChecksum {
			return fmt.Errorf(
				"No checksum provided for download: %s. "+
					"Checksum verification is mandatory for security. "+
					"Provide expected_sha256 or use allow_missing_checksum with confirmation.",
				url)
		} else {
			slog.Warn("No checksum available for download. Integrity cannot be verified.", "url", url)

			if d.ConfirmFn != nil {
				if !d.ConfirmFn("No checksum available for download. Integrity cannot be verified.") {
					return fmt.Errorf(
						"Download cancelled: %s (no checksum provided)",
						url)
				}
			} else {
				slog.Warn("No checksum available for download. Proceeding without confirmation.",
					"url", url)
			}
		}
	}

	// ── Setup SHA256 hashing + progress wrapping ──
	sha256Hash := sha256.New()
	downloaded := int64(0)
	totalSizeCell := int64(-1)

	onStart := func(totalSize int64) {
		totalSizeCell = totalSize
	}

	var chunkCallback ProgressFunc
	if progress != nil || expectedSHA256 != "" {
		chunkCallback = func(chunk []byte) {
			downloaded += int64(len(chunk))
			if progress != nil {
				progress(downloaded, totalSizeCell)
			}
			if expectedSHA256 != "" {
				sha256Hash.Write(chunk)
			}
		}
	}

	// ── Download via HTTP with retry ──
	if _, err := d.WithDownload(ctx, url, dest, chunkCallback, onStart); err != nil {
		return err
	}

	// ── SHA256 verification ──
	if expectedSHA256 != "" {
		actualSHA256 := hex.EncodeToString(sha256Hash.Sum(nil))
		if !strings.EqualFold(actualSHA256, expectedSHA256) {
			os.Remove(dest)
			return errs.New(
				errs.CodeImageChecksumMismatch,
				fmt.Sprintf("Checksum mismatch! Expected %s, got %s", expectedSHA256, actualSHA256),
			)
		}
		slog.Info("Checksum verified")
	}

	// ── Auto-populate the local asset mirror ──
	autoPopulateMirror(url, dest)

	return nil
}

// autoPopulateMirror copies a successfully downloaded file to the asset mirror.
func autoPopulateMirror(url, srcPath string) {
	mirrorDir, ok := infra.EnvGet("ASSET_MIRROR")
	if !ok || mirrorDir == "" {
		return
	}
	filename := extractFilename(url)
	mirrorDest := filepath.Join(mirrorDir, filename)
	info, err := os.Stat(mirrorDest)
	if err == nil && !info.IsDir() {
		return
	}
	if err := os.MkdirAll(filepath.Dir(mirrorDest), infra.DirPerm); err != nil {
		slog.Warn("Failed to create asset mirror dir", "error", err)
		return
	}
	if err := infra.CopyPreservingMetadata(srcPath, mirrorDest); err != nil {
		slog.Warn("Failed to copy to asset mirror", "path", mirrorDest, "error", err)
		return
	}
	slog.Info("Copied to asset mirror", "path", mirrorDest)
}

// ── GetJSON (read_json_content) ─────────────────────────────────────────

// GetJSON fetches a URL, parses the response as JSON, and returns the result.
// Mirrors Python's HttpDownload.read_json_content() which returns
// dict[str, Any] | list[Any] — already parsed JSON.
//
// Returns the parsed JSON value (typically map[string]any or []any).
// On error, returns a DomainError wrapping the underlying cause.
func (d *Downloader) GetJSON(
	ctx context.Context,
	url string,
	timeout int,
	headers map[string]string,
	useCache bool,
	cacheTTLSeconds int,
) (any, error) {
	cacheFile := d.cache.Path(url)

	if useCache && d.cache != nil {
		if d.cache.IsValid(cacheFile, cacheTTLSeconds) {
			data, err := d.cache.Read(cacheFile)
			if err == nil {
				var result any
				if jsonErr := json.Unmarshal(data, &result); jsonErr == nil {
					return result, nil
				}
			}
		}
	}

	defaultHeaders := map[string]string{
		"Accept": "application/json",
	}
	for k, v := range headers {
		defaultHeaders[k] = v
	}

	req, err := d.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for k, v := range defaultHeaders {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: %v", url, err), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errs.New(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: HTTP %d", url, resp.StatusCode))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: %v", url, err), err)
	}

	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, errs.WrapMsg(
			errs.CodeDownloadFailed,
			fmt.Sprintf("Failed to parse JSON from %s: %v", url, err),
			err,
		)
	}

	if useCache && d.cache != nil {
		if writeErr := d.cache.Write(body, cacheFile); writeErr != nil {
			slog.Warn("Failed to cache JSON response", "error", writeErr)
		}
	}

	return parsed, nil
}

// ── GetContent (read_raw_content) ─────────────────────────────────────────────

// GetContent fetches a URL and returns the raw response body as a string.
// Mirrors Python's HttpDownload.read_raw_content().
func (d *Downloader) GetContent(
	ctx context.Context,
	opts RequestOpts,
) (string, error) {
	cacheFile := d.cache.Path(opts.URL)

	if opts.UseCache && d.cache != nil {
		if d.cache.IsValid(cacheFile, opts.CacheTTLSeconds) {
			data, err := d.cache.Read(cacheFile)
			if err == nil {
				return string(data), nil
			}
		}
	}

	defaultHeaders := map[string]string{
		"Accept": "text/plain",
	}
	for k, v := range opts.Headers {
		defaultHeaders[k] = v
	}

	req, err := d.newRequest(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	for k, v := range defaultHeaders {
		req.Header.Set(k, v)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return "", errs.WrapMsg(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: %v", opts.URL, err), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errs.New(
			errs.CodeDownloadFailed,
			fmt.Sprintf("Failed to fetch %s: HTTP %d", opts.URL, resp.StatusCode),
		)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", errs.WrapMsg(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: %v", opts.URL, err), err)
	}

	if opts.UseCache && d.cache != nil {
		if writeErr := d.cache.Write(body, cacheFile); writeErr != nil {
			slog.Warn("Failed to cache raw response", "error", writeErr)
		}
	}

	return string(body), nil
}

// ── HeadSize (head_size) ─────────────────────────────────────────────────

// HeadSize sends a HEAD request and returns the Content-Length.
// Returns (0, false) when size is unavailable (matching Python's int | None).
// Mirrors Python's HttpDownload.head_size().
func (d *Downloader) HeadSize(
	ctx context.Context,
	opts RequestOpts,
) (size int64, ok bool) {
	cacheFile := d.cache.Path(opts.URL)

	if opts.UseCache && d.cache != nil {
		if d.cache.IsValid(cacheFile, opts.CacheTTLSeconds) {
			data, err := d.cache.Read(cacheFile)
			if err == nil && len(data) > 0 {
				var cachedSize int64
				if _, scanErr := fmt.Sscanf(string(data), "%d", &cachedSize); scanErr == nil && cachedSize >= 0 {
					return cachedSize, true
				}
			}
		}
	}

	req, err := d.newRequest(ctx, http.MethodHead, opts.URL, nil)
	if err != nil {
		return 0, false
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return 0, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, false
	}

	contentLength := resp.ContentLength
	if contentLength < 0 {
		return 0, false
	}

	if opts.UseCache && d.cache != nil {
		cacheData := fmt.Sprintf("%d", contentLength)
		if writeErr := d.cache.Write([]byte(cacheData), cacheFile); writeErr != nil {
			slog.Warn("Failed to cache HEAD response", "error", writeErr)
		}
	}

	return contentLength, true
}

// ── GetBody (simple fetch) ───────────────────────────────────────────────

// GetBody fetches a URL and returns the raw response body as bytes.
// Mirrors Python's HttpDownload._download() for simple cases.
func (d *Downloader) GetBody(ctx context.Context, url string) ([]byte, error) {
	req, err := d.newRequest(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: %v", url, err), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errs.New(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: HTTP %d", url, resp.StatusCode))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDownloadFailed, fmt.Sprintf("Failed to fetch %s: %v", url, err), err)
	}

	return data, nil
}
