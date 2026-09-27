package basispoints

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScaleImageColdReservationCanBeSharedAtCapacity(t *testing.T) {
	for _, quota := range []string{"entries", "bytes"} {
		t.Run(quota, func(t *testing.T) {
			options := ImageRelayOptions{MaxEntries: 1}
			if quota == "bytes" {
				options = ImageRelayOptions{MaxEntries: 2, MaxBytes: 1 << 20}
			}
			r, err := NewImageRelay("https://images.example", t.TempDir(), options)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, r.Close()) })
			data := make([]byte, 600<<10)
			copy(data, relayTestPNG(t))
			dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
			// Pause the first batch after its image is ready but before it has
			// committed. This is the actual cold-upload concurrency window.
			first, token, err := r.storeImage(dataURL, "scope")
			require.NoError(t, err)
			defer func() { r.mu.Lock(); r.releaseStagedLocked(first); r.mu.Unlock() }()
			require.Empty(t, r.entries)
			require.Equal(t, 1, r.reservedEntries)
			raw := relayTestRequest(t, data)
			var wg sync.WaitGroup
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := r.Rewrite(raw, "scope")
					if err != nil {
						t.Errorf("identical cold image rejected despite existing reservation: %v", err)
					}
				}()
			}
			wg.Wait()
			// The first batch can fail after another batch has committed the
			// same immutable file; releasing it must not delete that file.
			r.mu.Lock()
			r.releaseStagedLocked(first)
			r.mu.Unlock()
			require.Len(t, r.entries, 1)
			require.Zero(t, r.reservedEntries)
			require.Zero(t, r.reservedBytes)
			require.Equal(t, len(data), r.bytes)
			files, err := os.ReadDir(r.dir)
			require.NoError(t, err)
			require.Len(t, files, 1)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, r.baseURL+ImageRelayPath+token, nil))
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, data, w.Body.Bytes())
		})
	}
}

func TestScaleImageColdMIMEAliasesShareOneBatchReservation(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxEntries: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(relayTestPNG(t))
	parts := []any{object{"type": "input_image", "image_url": dataURL, "detail": "original"}, object{"type": "input_image", "image_url": strings.Replace(dataURL, "image/png", "application/octet-stream", 1), "detail": "low"}}
	raw, err := json.Marshal(object{"input": []any{object{"role": "user", "content": parts}}})
	require.NoError(t, err)
	out, err := r.Rewrite(raw, "scope")
	require.NoError(t, err, "same-batch aliases must not wait for their own batch to commit")
	var result object
	require.NoError(t, decode(out, &result))
	actual := result["input"].([]any)[0].(object)["content"].([]any)
	require.Equal(t, actual[0].(object)["image_url"], actual[1].(object)["image_url"])
	require.Equal(t, "original", actual[0].(object)["detail"])
	require.Equal(t, "low", actual[1].(object)["detail"])
	require.Len(t, r.entries, 1)
	require.Zero(t, r.reservedEntries)
	for _, img := range r.entries {
		require.Zero(t, img.pins)
	}
}

func TestScaleImageColdSharedAbortPreservesOtherBatch(t *testing.T) {
	r, err := NewImageRelay("https://images.example", t.TempDir(), ImageRelayOptions{MaxEntries: 1})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Close()) })
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(relayTestPNG(t))
	first, _, err := r.storeImage(dataURL, "scope")
	require.NoError(t, err)
	defer func() { r.mu.Lock(); r.releaseStagedLocked(first); r.mu.Unlock() }()
	second, _, err := r.storeImage(dataURL, "scope")
	require.NoError(t, err)
	defer func() { r.mu.Lock(); r.releaseStagedLocked(second); r.mu.Unlock() }()
	r.mu.Lock()
	r.releaseStagedLocked(first)
	r.mu.Unlock()
	_, err = os.Stat(second.path)
	require.NoError(t, err, "aborting one owner must not delete another batch's image")
	require.Equal(t, 1, r.reservedEntries)
	_, _, err = r.storeImage(dataURL, "different-key-scope")
	require.ErrorIs(t, err, ErrImageRelayFull, "sharing must never cross authorization scope")
	r.mu.Lock()
	r.releaseStagedLocked(second)
	r.mu.Unlock()
	require.Empty(t, r.entries)
	require.Zero(t, r.bytes)
	require.Zero(t, r.reservedEntries)
	require.Zero(t, r.reservedBytes)
	files, err := os.ReadDir(r.dir)
	require.NoError(t, err)
	require.Empty(t, files)
}
