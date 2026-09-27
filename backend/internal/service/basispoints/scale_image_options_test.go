package basispoints

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestScaleImageOptionsDefaultsAndValidation(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	require.Equal(t, imageRelayMaxEntries, r.maxEntries)
	require.Equal(t, imageRelayMaxBytes, r.maxBytes)
	require.Equal(t, 32, cap(r.downloads))
	require.Equal(t, 2*time.Minute, r.downloadTimeout)
	for _, options := range []ImageRelayOptions{
		{MaxEntries: -1}, {MaxEntries: 100_001},
		{MaxBytes: -1}, {MaxBytes: (1 << 20) - 1}, {MaxBytes: (64 << 30) + 1},
		{MaxDownloads: -1}, {MaxDownloads: 257},
		{DownloadTimeout: -1}, {DownloadTimeout: time.Second - 1}, {DownloadTimeout: 10*time.Minute + 1},
	} {
		root := filepath.Join(t.TempDir(), "uncreated")
		invalid, err := NewImageRelay("https://images.example", root, options)
		require.Error(t, err, "options=%+v", options)
		require.Nil(t, invalid)
		_, err = os.Stat(root)
		require.True(t, os.IsNotExist(err), "reject invalid limits before creating storage")
	}
	_, err = NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{}, ImageRelayOptions{})
	require.Error(t, err)
}

type scaleDeadlineCaptureWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	setError  error
}

func (w *scaleDeadlineCaptureWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return w.setError
}

func TestScaleImageDownloadDeadlineHeadersAndSetterFailure(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{DownloadTimeout: time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	data := relayTestPNG(t)
	out, err := r.Rewrite(relayTestRequest(t, data), "scope")
	require.NoError(t, err)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := &scaleDeadlineCaptureWriter{ResponseRecorder: httptest.NewRecorder()}
		started := time.Now()
		r.ServeHTTP(w, httptest.NewRequest(method, relayTestURL(t, out), nil))
		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, "image/png", w.Header().Get("Content-Type"))
		require.Len(t, w.deadlines, 2)
		require.WithinDuration(t, started.Add(time.Second), w.deadlines[0], 500*time.Millisecond)
		require.True(t, w.deadlines[1].IsZero())
		require.True(t, w.Flushed, "headers and final bytes must be flushed before clearing the deadline")
		if method == http.MethodGet {
			require.Equal(t, data, w.Body.Bytes())
		} else {
			require.Empty(t, w.Body.Bytes())
		}
		require.Empty(t, r.downloads)
	}
	w := &scaleDeadlineCaptureWriter{ResponseRecorder: httptest.NewRecorder(), setError: errors.New("network deadline failure")}
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, relayTestURL(t, out), nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code, "a failed network deadline must not become misleading image success")
	require.Equal(t, "1", w.Header().Get("Retry-After"))
	require.Empty(t, w.Body.Bytes())
	require.Empty(t, r.downloads)
	for _, img := range r.entries {
		require.Zero(t, img.readers)
	}
}

func TestScaleImageHasTokenOwnershipIsReadOnly(t *testing.T) {
	var disabled *ImageRelay
	require.False(t, disabled.HasToken(strings.Repeat("a", 43)))
	r, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	out, err := r.Rewrite(relayTestRequest(t, relayTestPNG(t)), "scope")
	require.NoError(t, err)
	token := strings.TrimPrefix(relayTestURL(t, out), r.baseURL+ImageRelayPath)
	img := r.entries[token]
	beforeExpiry, beforeBytes := img.expires, r.bytes
	require.True(t, r.HasToken(token))
	require.False(t, r.HasToken("short"))
	require.False(t, r.HasToken(strings.Repeat("a", 43)))
	require.Equal(t, beforeExpiry, img.expires)
	require.Equal(t, beforeBytes, r.bytes)
	require.Zero(t, r.reservedBytes)
	require.Zero(t, img.readers)
	img.expires = time.Now().Add(-time.Second)
	img.pins = 1
	require.False(t, r.HasToken(token), "expired pinned files are not available to a new HTTP request")
	require.Len(t, r.entries, 1, "ownership checks must not mutate expiry state")
	img.pins = 0
	img.expires = beforeExpiry
	require.NoError(t, r.Close())
	require.False(t, r.HasToken(token))
}

func TestScaleImageConfiguredEntryAndDownloadLimits(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxEntries: 1, MaxBytes: 1 << 20, MaxDownloads: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	raw := relayTestRequest(t, relayTestPNG(t))
	out, err := r.Rewrite(raw, "scope-1")
	require.NoError(t, err)
	_, err = r.Rewrite(raw, "scope-2")
	require.ErrorIs(t, err, ErrImageRelayFull)
	again, err := r.Rewrite(raw, "scope-1")
	require.NoError(t, err)
	require.Equal(t, out, again)
	r.downloads <- struct{}{}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, relayTestURL(t, out), nil))
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	<-r.downloads
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, relayTestURL(t, out), nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, relayTestPNG(t), w.Body.Bytes())
	require.Len(t, r.sources, 1)
	for _, img := range r.entries {
		img.expires = time.Now().Add(-time.Second)
	}
	_, err = r.Rewrite(raw, "scope-2")
	require.NoError(t, err, "quota pressure must reclaim expired files promptly")
	require.Len(t, r.entries, 1)
	require.Len(t, r.sources, 1, "the fingerprint index must expire with its entry")
}

func TestScaleImageConfiguredByteLimit(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxBytes: 1 << 20})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	data := make([]byte, 600<<10)
	copy(data, relayTestPNG(t))
	raw := relayTestRequest(t, data)
	_, err = r.Rewrite(raw, "scope-1")
	require.NoError(t, err)
	_, err = r.Rewrite(raw, "scope-2")
	require.ErrorIs(t, err, ErrImageRelayFull)
	_, err = r.Rewrite(raw, "scope-1")
	require.NoError(t, err)
	require.Equal(t, len(data), r.bytes)
	require.Zero(t, r.reservedBytes)
}

func TestScaleImageCachePinProtectsUncommittedReuse(t *testing.T) {
	r, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	raw := relayTestRequest(t, relayTestPNG(t))
	out, err := r.Rewrite(raw, "scope")
	require.NoError(t, err)
	token := strings.TrimPrefix(relayTestURL(t, out), r.baseURL+ImageRelayPath)
	var source object
	require.NoError(t, decode(raw, &source))
	item := source["input"].([]any)[0].(object)
	part := item["content"].([]any)[1].(object)
	cached, gotToken, err := r.storeImage(text(part["image_url"]), "scope")
	require.NoError(t, err)
	require.Equal(t, token, gotToken)
	original := r.entries[token]
	require.Same(t, original, cached.reused)
	r.mu.Lock()
	original.expires = time.Now().Add(-time.Second)
	r.pruneLocked(time.Now())
	r.mu.Unlock()
	require.Same(t, original, r.entries[token])
	_, err = os.Stat(original.path)
	require.NoError(t, err, "staged reuse must retain its immutable file through expiry")
	r.mu.Lock()
	r.releaseStagedLocked(cached)
	r.mu.Unlock()
	require.Empty(t, r.entries)
	require.Empty(t, r.sources)
	require.Zero(t, r.bytes)
	_, err = os.Stat(original.path)
	require.True(t, os.IsNotExist(err))
}
