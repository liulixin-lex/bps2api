package basispoints

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

var (
	ErrImageRelayFull    = errors.New("basispoints temporary image storage is full; retry after images expire")
	ErrImageRelayStorage = errors.New("basispoints temporary image storage is unavailable")
)

const (
	ImageRelayPath             = "/api/bps-images/"
	imageRelayTTL              = 30 * time.Minute
	imageRelayMaxBytes         = 1 << 30
	imageRelayMaxEntries       = 512
	imageRelayMaxImageBytes    = 20 << 20
	imageRelayMaxRequestBytes  = 50_000_000
	imageRelayMaxRequestImages = 500
	imageRelayMaxPixels        = 64 * 1024 * 1024
	imageRelayCleanupInterval  = time.Minute
	imageRelayDownloadTimeout  = 2 * time.Minute
)

// ImageRelayOptions bounds retained disk data and concurrent downloads per
// process. Zero preserves the default. Limits are fixed for an instance's life;
// callers should size them against available disk and proxy bandwidth.
type ImageRelayOptions struct {
	MaxEntries      int
	MaxBytes        int64
	MaxDownloads    int
	DownloadTimeout time.Duration
}

func resolveImageRelayOptions(options []ImageRelayOptions) (ImageRelayOptions, error) {
	if len(options) > 1 {
		return ImageRelayOptions{}, fmt.Errorf("image relay accepts at most one options object")
	}
	var limits ImageRelayOptions
	if len(options) == 1 {
		limits = options[0]
	}
	if limits.MaxEntries == 0 {
		limits.MaxEntries = imageRelayMaxEntries
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = imageRelayMaxBytes
	}
	if limits.MaxDownloads == 0 {
		limits.MaxDownloads = 32
	}
	if limits.DownloadTimeout == 0 {
		limits.DownloadTimeout = imageRelayDownloadTimeout
	}
	if limits.MaxEntries < 1 || limits.MaxEntries > 100_000 {
		return ImageRelayOptions{}, fmt.Errorf("image relay max entries must be between 1 and 100000")
	}
	if limits.MaxBytes < 1<<20 || limits.MaxBytes > 64<<30 || int64(int(limits.MaxBytes)) != limits.MaxBytes {
		return ImageRelayOptions{}, fmt.Errorf("image relay max bytes must be between 1 MiB and 64 GiB and supported by this platform")
	}
	if limits.MaxDownloads < 1 || limits.MaxDownloads > 256 {
		return ImageRelayOptions{}, fmt.Errorf("image relay max downloads must be between 1 and 256")
	}
	if limits.DownloadTimeout < time.Second || limits.DownloadTimeout > 10*time.Minute {
		return ImageRelayOptions{}, fmt.Errorf("image relay download timeout must be between 1 second and 10 minutes")
	}
	return limits, nil
}

// Image bytes live in private disk files; only bounded metadata lives on heap.
// Pending uploads reserve disk capacity before decoding. Files are never exposed
// as a static directory and downloads use bounded streaming buffers.
type ImageRelay struct {
	baseURL         string
	key             [32]byte
	mu              sync.Mutex
	entries         map[string]*relayImage
	sources         map[[32]byte]string
	bytes           int
	reservedBytes   int
	reservedEntries int
	retiredEntries  int
	root            string
	dir             string
	closed          bool
	stop            chan struct{}
	done            chan struct{}
	downloads       chan struct{}
	maxEntries      int
	maxBytes        int
	pruneAfter      time.Time
	downloadTimeout time.Duration
}

type relayImage struct {
	path        string
	size        int
	reserved    int
	contentType string
	expires     time.Time
	readers     int
	retired     bool
	sourceKey   [32]byte
	pins        int
	reused      *relayImage
}

func ValidateImageRelayOrigin(baseURL string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(baseURL, "#") || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("BPS image relay requires an HTTPS public origin without credentials, path, query or fragment")
	}
	return nil
}

func NewImageRelay(baseURL, storageRoot string, options ...ImageRelayOptions) (*ImageRelay, error) {
	if err := ValidateImageRelayOrigin(baseURL); err != nil {
		return nil, err
	}
	if storageRoot == "" {
		return nil, ErrImageRelayStorage
	}
	limits, err := resolveImageRelayOptions(options)
	if err != nil {
		return nil, err
	}
	relay := &ImageRelay{baseURL: strings.TrimRight(baseURL, "/"), entries: make(map[string]*relayImage), sources: make(map[[32]byte]string), root: storageRoot, stop: make(chan struct{}), done: make(chan struct{}), downloads: make(chan struct{}, limits.MaxDownloads), maxEntries: limits.MaxEntries, maxBytes: int(limits.MaxBytes), downloadTimeout: limits.DownloadTimeout}
	if _, err := rand.Read(relay.key[:]); err != nil {
		return nil, ErrImageRelayStorage
	}
	if err := os.MkdirAll(storageRoot, 0700); err != nil {
		return nil, ErrImageRelayStorage
	}
	dir, err := os.MkdirTemp(storageRoot, "session-")
	if err != nil {
		return nil, ErrImageRelayStorage
	}
	relay.dir = dir
	relay.cleanupOrphans(time.Now())
	go relay.cleanupLoop()
	return relay, nil
}

func (r *ImageRelay) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		close(r.stop)
	}
	r.mu.Unlock()
	<-r.done
	if err := os.RemoveAll(r.dir); err != nil {
		return ErrImageRelayStorage
	}
	return nil
}

func (r *ImageRelay) cleanupLoop() {
	defer close(r.done)
	ticker := time.NewTicker(imageRelayCleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case now := <-ticker.C:
			r.mu.Lock()
			r.pruneLocked(now)
			_ = os.Chtimes(r.dir, now, now)
			r.mu.Unlock()
			r.cleanupOrphans(now)
		}
	}
}

// Each live instance refreshes its directory every minute. After an abnormal
// exit, a later/running instance removes directories idle for more than the TTL.
func (r *ImageRelay) cleanupOrphans(now time.Time) {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join(r.root, entry.Name())
		if path == r.dir || !entry.IsDir() || !strings.HasPrefix(entry.Name(), "session-") {
			continue
		}
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > imageRelayTTL+imageRelayCleanupInterval {
			_ = os.RemoveAll(path)
		}
	}
}

// SetPublicOrigin changes new links without discarding in-flight images.
func (r *ImageRelay) SetPublicOrigin(baseURL string) error {
	if err := ValidateImageRelayOrigin(baseURL); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrImageRelayStorage
	}
	r.baseURL = strings.TrimRight(baseURL, "/")
	return nil
}

func (r *ImageRelay) Rewrite(raw []byte, scope string) ([]byte, error) {
	if r == nil {
		return raw, nil
	}
	r.mu.Lock()
	baseURL, closed := r.baseURL, r.closed
	r.mu.Unlock()
	if closed {
		return nil, ErrImageRelayStorage
	}
	var source object
	if err := decode(raw, &source); err != nil || source == nil {
		return nil, fmt.Errorf("invalid Basispoints request JSON")
	}
	input, _ := source["input"].([]any)
	images := make(map[string]*relayImage)
	// Repeated images in expanded Codex history share one staged file.
	seenURLs := make(map[string]string)
	imageCount := 0
	var staged []*relayImage
	// Every failed/duplicate batch releases both files and quota.
	defer func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, img := range staged {
			r.releaseStagedLocked(img)
		}
	}()
	totalBytes := 0
	for _, rawItem := range input {
		item, _ := rawItem.(object)
		for _, field := range []string{"content", "output"} {
			if field == "output" && text(item["type"]) != "function_call_output" && text(item["type"]) != "custom_tool_call_output" {
				continue
			}
			parts, _ := item[field].([]any)
			for _, rawPart := range parts {
				part, _ := rawPart.(object)
				normalizeContentPart(part)
				if text(part["type"]) != "input_image" {
					continue
				}
				rawURL := text(part["image_url"])
				if len(rawURL) < len("data:") || !strings.EqualFold(rawURL[:len("data:")], "data:") {
					continue
				}
				imageCount++
				if imageCount > imageRelayMaxRequestImages {
					return nil, fmt.Errorf("basispoints accepts at most 500 inline images per request")
				}
				if token, ok := seenURLs[rawURL]; ok {
					part["image_url"] = baseURL + ImageRelayPath + token
					if err := validateImage(part); err != nil {
						return nil, err
					}
					continue
				}
				img, token, err := r.storeImage(rawURL, scope)
				if err != nil {
					return nil, err
				}
				staged = append(staged, img)
				if images[token] == nil {
					totalBytes += img.size
				}
				if totalBytes > imageRelayMaxRequestBytes {
					return nil, fmt.Errorf("basispoints unique inline images exceed the 50 MB request limit")
				}
				seenURLs[rawURL] = token
				part["image_url"] = baseURL + ImageRelayPath + token
				if err := validateImage(part); err != nil {
					return nil, err
				}
				images[token] = img
			}
		}
	}
	if len(staged) == 0 {
		return raw, nil
	}
	out, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("encode basispoints image request")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrImageRelayStorage
	}
	now := time.Now()
	r.pruneDueLocked(now)
	for token, img := range images {
		if existing := r.entries[token]; existing != nil {
			existing.expires = now.Add(imageRelayTTL)
			continue
		}
		r.reservedBytes -= img.reserved
		r.reservedEntries--
		img.reserved = 0
		img.expires = now.Add(imageRelayTTL)
		r.entries[token] = img
		r.sources[img.sourceKey] = token
		r.bytes += img.size
	}
	return out, nil
}

func relayImagePayload(raw string) (string, string, error) {
	header, payload, ok := strings.Cut(raw[len("data:"):], ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") {
		return "", "", fmt.Errorf("basispoints inline image requires a base64 image data URL")
	}
	declared, params, err := mime.ParseMediaType(header[:len(header)-len(";base64")])
	if err != nil || len(params) != 0 {
		return "", "", fmt.Errorf("basispoints inline image has an invalid media type")
	}
	switch declared {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "application/octet-stream":
	default:
		return "", "", fmt.Errorf("basispoints inline images must be PNG, JPEG, GIF or WebP")
	}
	if len(payload) > base64.StdEncoding.EncodedLen(imageRelayMaxImageBytes) {
		return "", "", fmt.Errorf("basispoints inline image exceeds the 20 MiB limit")
	}
	if payload == "" {
		return "", "", fmt.Errorf("basispoints inline image contains invalid base64 data")
	}
	return declared, payload, nil
}

func (r *ImageRelay) storeImage(raw, scope string) (*relayImage, string, error) {
	declared, payload, err := relayImagePayload(raw)
	if err != nil {
		return nil, "", err
	}
	// Conversation history frequently repeats the exact base64 payload. A bounded
	// fingerprint index avoids decoding, disk writes and temporary quota for an
	// image that is already retained. The public token still hashes decoded bytes.
	// Excluding the declared MIME allows Codex's octet-stream and image aliases
	// to share a cache entry; an explicit MIME must still match the verified type.
	fingerprint := sha256.New()
	_, _ = fingerprint.Write([]byte(scope))
	_, _ = fingerprint.Write([]byte{0})
	_, _ = io.CopyBuffer(fingerprint, io.LimitReader(strings.NewReader(payload), int64(len(payload))), make([]byte, 32*1024))
	var sourceKey [32]byte
	copy(sourceKey[:], fingerprint.Sum(nil))
	reservation := base64.StdEncoding.DecodedLen(len(payload)) + 1
	r.mu.Lock()
	now := time.Now()
	r.pruneDueLocked(now)
	if r.closed {
		r.mu.Unlock()
		return nil, "", ErrImageRelayStorage
	}
	if token, ok := r.sources[sourceKey]; ok {
		if existing := r.entries[token]; existing != nil && existing.pins == 0 && !now.Before(existing.expires) {
			r.removeLocked(token, existing)
		}
		if existing := r.entries[token]; existing != nil {
			if declared != "application/octet-stream" && declared != existing.contentType {
				r.mu.Unlock()
				return nil, "", fmt.Errorf("basispoints inline image media type does not match its contents")
			}
			// Pruning may run while the remainder of the request is validated. Pin
			// the existing file until Rewrite either commits or aborts the batch.
			existing.pins++
			cached := &relayImage{path: existing.path, size: existing.size, contentType: existing.contentType, reused: existing}
			r.mu.Unlock()
			return cached, token, nil
		}
	}
	if int64(r.bytes)+int64(r.reservedBytes)+int64(reservation) > int64(r.maxBytes) || len(r.entries)+r.reservedEntries+r.retiredEntries >= r.maxEntries {
		// Reclaim expired entries before refusing genuinely new data. Full-map
		// scans are otherwise limited to the cleanup cadence, not every image.
		r.pruneLocked(now)
	}
	if int64(r.bytes)+int64(r.reservedBytes)+int64(reservation) > int64(r.maxBytes) || len(r.entries)+r.reservedEntries+r.retiredEntries >= r.maxEntries {
		r.mu.Unlock()
		return nil, "", ErrImageRelayFull
	}
	r.reservedBytes += reservation
	r.reservedEntries++
	r.mu.Unlock()
	img := &relayImage{reserved: reservation, sourceKey: sourceKey}
	success := false
	defer func() {
		if !success {
			r.mu.Lock()
			r.discardLocked(img)
			r.mu.Unlock()
		}
	}()
	file, err := os.CreateTemp(r.dir, "image-")
	if err != nil {
		return nil, "", ErrImageRelayStorage
	}
	img.path = file.Name()
	defer func() { _ = file.Close() }()
	mac := hmac.New(sha256.New, r.key[:])
	_, _ = mac.Write([]byte(scope))
	_, _ = mac.Write([]byte{0})
	reader := base64.NewDecoder(base64.StdEncoding, strings.NewReader(payload))
	n, err := io.CopyBuffer(io.MultiWriter(file, mac), io.LimitReader(reader, imageRelayMaxImageBytes+1), make([]byte, 32*1024))
	if err != nil {
		var corrupt base64.CorruptInputError
		if errors.As(err, &corrupt) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, "", fmt.Errorf("basispoints inline image contains invalid base64 data")
		}
		return nil, "", ErrImageRelayStorage
	}
	if n == 0 {
		return nil, "", fmt.Errorf("basispoints inline image contains invalid base64 data")
	}
	if n > imageRelayMaxImageBytes {
		return nil, "", fmt.Errorf("basispoints inline image exceeds the 20 MiB limit")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", ErrImageRelayStorage
	}
	dimensions, format, err := image.DecodeConfig(file)
	if err != nil || dimensions.Width <= 0 || dimensions.Height <= 0 || int64(dimensions.Width)*int64(dimensions.Height) > imageRelayMaxPixels {
		return nil, "", fmt.Errorf("basispoints inline image is invalid or exceeds 64 megapixels")
	}
	detected := "image/" + format
	switch detected {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return nil, "", fmt.Errorf("basispoints inline images must be PNG, JPEG, GIF or WebP")
	}
	// Codex emits generic binary data URLs; serve only a verified image MIME.
	if declared != "application/octet-stream" && detected != declared {
		return nil, "", fmt.Errorf("basispoints inline image media type does not match its contents")
	}
	if err := file.Close(); err != nil {
		return nil, "", ErrImageRelayStorage
	}
	img.size, img.contentType = int(n), detected
	success = true
	return img, base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (r *ImageRelay) discardLocked(img *relayImage) {
	if img.path != "" {
		_ = os.Remove(img.path)
	}
	r.reservedBytes -= img.reserved
	r.reservedEntries--
	img.reserved = 0
}

func (r *ImageRelay) releaseStagedLocked(img *relayImage) {
	if img.reused == nil {
		if img.reserved > 0 {
			r.discardLocked(img)
		}
		return
	}
	existing := img.reused
	img.reused = nil
	existing.pins--
	if existing.pins == 0 && !time.Now().Before(existing.expires) {
		if token := r.sources[existing.sourceKey]; r.entries[token] == existing {
			r.removeLocked(token, existing)
		}
	}
}

func (r *ImageRelay) removeLocked(token string, img *relayImage) {
	if img.pins > 0 {
		return
	}
	if r.sources[img.sourceKey] == token {
		delete(r.sources, img.sourceKey)
	}
	if img.readers > 0 {
		img.retired = true
		r.retiredEntries++
		delete(r.entries, token)
		return
	}
	_ = os.Remove(img.path)
	r.bytes -= img.size
	delete(r.entries, token)
}

func (r *ImageRelay) finishRead(img *relayImage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	img.readers--
	if img.retired && img.readers == 0 {
		_ = os.Remove(img.path)
		r.bytes -= img.size
		r.retiredEntries--
	}
}

func (r *ImageRelay) pruneLocked(now time.Time) {
	for token, img := range r.entries {
		if !now.Before(img.expires) {
			r.removeLocked(token, img)
		}
	}
	r.pruneAfter = now.Add(imageRelayCleanupInterval)
}

func (r *ImageRelay) pruneDueLocked(now time.Time) {
	if !now.Before(r.pruneAfter) {
		r.pruneLocked(now)
	}
}

func (r *ImageRelay) expire(token string, expected *relayImage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if img := r.entries[token]; img == expected && img != nil && !time.Now().Before(img.expires) {
		r.removeLocked(token, img)
	}
}

// HasToken reports whether this instance owns an unexpired image capability. It
// neither serves bytes nor changes retention, quota or files. An expired pinned
// image remains unavailable, just as in ServeHTTP.
func (r *ImageRelay) HasToken(token string) bool {
	if r == nil || len(token) != 43 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	img := r.entries[token]
	return !r.closed && img != nil && time.Now().Before(img.expires)
}

type relayImageContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r relayImageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (r *ImageRelay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token, ok := strings.CutPrefix(req.URL.Path, ImageRelayPath)
	if r == nil || !ok || len(token) != 43 {
		http.NotFound(w, req)
		return
	}
	r.mu.Lock()
	img := r.entries[token]
	if img != nil && !time.Now().Before(img.expires) {
		r.removeLocked(token, img)
		img = nil
	}
	if img == nil || r.closed {
		r.mu.Unlock()
		http.NotFound(w, req)
		return
	}
	// An unknown or expired capability must remain a 404 even when downloads
	// are saturated, so reverse proxies can try the instance that owns it.
	select {
	case r.downloads <- struct{}{}:
		defer func() { <-r.downloads }()
	default:
		r.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	file, err := os.Open(img.path)
	if err == nil {
		img.readers++
	}
	r.mu.Unlock()
	if err != nil {
		http.NotFound(w, req)
		return
	}
	defer func() {
		_ = file.Close()
		r.finishRead(img)
	}()
	if req.Context().Err() != nil {
		w.WriteHeader(http.StatusRequestTimeout)
		return
	}
	// Bound an image download's actual network writes, including the final
	// buffered flush. A slow reader must not pin one of the limited slots or
	// retain an expired file indefinitely. This is scoped to this response, not
	// the server's long-lived Responses streams or subsequent keep-alive calls.
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(r.downloadTimeout)
	if contextDeadline, ok := req.Context().Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := controller.SetWriteDeadline(deadline); err == nil {
		cancelDone := make(chan struct{})
		stopCancel := context.AfterFunc(req.Context(), func() {
			_ = controller.SetWriteDeadline(time.Now())
			close(cancelDone)
		})
		defer func() {
			// Wait for an already-running cancellation callback before clearing
			// the deadline; no callback may touch a reused response writer.
			if !stopCancel() {
				<-cancelDone
			}
			_ = controller.SetWriteDeadline(time.Time{})
		}()
	} else if !errors.Is(err, http.ErrNotSupported) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	// In-memory/custom writers may not support deadlines. Context checks still
	// stop further reads, but only a network deadline can interrupt a blocked
	// Write. Production wrappers must preserve the Unwrap chain.
	w.Header().Set("Content-Type", img.contentType)
	w.Header().Set("Content-Length", strconv.Itoa(img.size))
	w.WriteHeader(http.StatusOK)
	if req.Method == http.MethodGet {
		if _, err := io.Copy(w, relayImageContextReader{ctx: req.Context(), reader: file}); err != nil {
			return
		}
	}
	_ = controller.Flush()
}
