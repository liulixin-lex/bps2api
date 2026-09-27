package basispoints

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Repeated conversation history consumes no additional retained image capacity.
func TestScaleImageCachedHistoryAtCapacity(t *testing.T) {
	for _, capacity := range []string{"entries", "bytes"} {
		t.Run(capacity, func(t *testing.T) {
			r, err := newTestImageRelay(t, "https://images.example")
			require.NoError(t, err)
			raw := relayTestRequest(t, relayTestPNG(t))
			initial, err := r.Rewrite(raw, "same-user-conversation")
			require.NoError(t, err)
			if capacity == "entries" {
				for i := len(r.entries); i < imageRelayMaxEntries; i++ {
					r.entries[fmt.Sprint(i)] = &relayImage{expires: time.Now().Add(time.Hour)}
				}
			} else {
				r.bytes = imageRelayMaxBytes
			}
			beforeEntries, beforeBytes := len(r.entries), r.bytes
			var wg sync.WaitGroup
			for i := 0; i < 64; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					actual, err := r.Rewrite(raw, "same-user-conversation")
					if err != nil {
						t.Errorf("cached history unexpectedly rejected: %v", err)
						return
					}
					if !bytes.Equal(initial, actual) {
						t.Error("cached image capability changed")
					}
				}()
			}
			wg.Wait()
			require.Equal(t, beforeEntries, len(r.entries))
			require.Equal(t, beforeBytes, r.bytes)
			require.Zero(t, r.reservedEntries)
			require.Zero(t, r.reservedBytes)
			files, err := os.ReadDir(r.dir)
			require.NoError(t, err)
			require.Len(t, files, 1, "cache hits must not create duplicate files")
			_, err = r.Rewrite(raw, "different-user-conversation")
			require.ErrorIs(t, err, ErrImageRelayFull, "capacity must remain bounded for uncached images")
		})
	}
}

func TestScaleImageCachedMetadataValidationAndAbort(t *testing.T) {
	r, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	raw := relayTestRequest(t, relayTestPNG(t))
	initial, err := r.Rewrite(raw, "scope")
	require.NoError(t, err)
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte("data:image/png;"), []byte("data:image/jpeg;"), 1),
		bytes.Replace(raw, []byte("\"high\""), []byte("\"INVALID\""), 1),
	} {
		_, err := r.Rewrite(bad, "scope")
		require.Error(t, err)
	}
	for i := 0; i < 8; i++ {
		_, err = r.Rewrite(bytes.Replace(raw, []byte("\"high\""), []byte("\"INVALID\""), 1), "scope")
		require.Error(t, err)
	}
	r.mu.Lock()
	for _, img := range r.entries {
		img.expires = time.Now().Add(-time.Second)
	}
	r.pruneLocked(time.Now())
	r.mu.Unlock()
	require.Empty(t, r.entries, "aborted cache hits must release their expiry pins")
	require.Zero(t, r.bytes)
	require.Zero(t, r.reservedEntries)
	require.Zero(t, r.reservedBytes)
	fresh, err := r.Rewrite(raw, "scope")
	require.NoError(t, err)
	require.Equal(t, initial, fresh)
}

func TestScaleImageBusyDownloadKeepsUnknownAndExpiredStatus(t *testing.T) {
	r, err := newTestImageRelay(t, "https://images.example")
	require.NoError(t, err)
	out, err := r.Rewrite(relayTestRequest(t, relayTestPNG(t)), "scope")
	require.NoError(t, err)
	knownURL := relayTestURL(t, out)
	for i := 0; i < cap(r.downloads); i++ {
		r.downloads <- struct{}{}
	}
	unknown := httptest.NewRecorder()
	r.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, r.baseURL+ImageRelayPath+strings.Repeat("x", 43), nil))
	require.Equal(t, http.StatusNotFound, unknown.Code, "unknown tokens must preserve fallback routing even while another instance is busy")
	known := httptest.NewRecorder()
	r.ServeHTTP(known, httptest.NewRequest(http.MethodGet, knownURL, nil))
	require.Equal(t, http.StatusServiceUnavailable, known.Code)
	require.Equal(t, "1", known.Header().Get("Retry-After"))
	r.mu.Lock()
	for _, img := range r.entries {
		img.expires = time.Now().Add(-time.Second)
	}
	r.mu.Unlock()
	expired := httptest.NewRecorder()
	r.ServeHTTP(expired, httptest.NewRequest(http.MethodGet, knownURL, nil))
	require.Equal(t, http.StatusNotFound, expired.Code)
	require.Zero(t, r.bytes)
}

func BenchmarkScaleImageCachedHistory(b *testing.B) {
	r, err := NewImageRelay("https://images.example", b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = r.Close() })
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		b.Fatal(err)
	}
	data := make([]byte, 1<<20)
	copy(data, pngBytes.Bytes())
	raw := []byte(fmt.Sprintf("{\"input\":[{\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64,%s\",\"detail\":\"original\"}]}]}", base64.StdEncoding.EncodeToString(data)))
	if _, err = r.Rewrite(raw, "scope"); err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := r.Rewrite(raw, "scope"); err != nil {
			b.Fatal(err)
		}
	}
}
