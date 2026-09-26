package httputil

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
)

func TestBoundedBodyIncludesPrereadAndIdentity(t *testing.T) {
	for _, preread := range []bool{false, true} {
		var body io.ReadCloser = io.NopCloser(bytes.NewReader([]byte("12345")))
		if preread {
			body = NewPrereadBody([]byte("12345"))
		}
		req := httptest.NewRequest("POST", "/", body)
		req.ContentLength = -1
		_, err := ReadRequestBodyBounded(httptest.NewRecorder(), req, 4)
		var max *http.MaxBytesError
		require.ErrorAs(t, err, &max)
	}
}

func TestDecompressedBodyExactBoundary(t *testing.T) {
	for _, size := range []int{maxDecompressedBodySize, maxDecompressedBodySize + 1} {
		payload := bytes.Repeat([]byte("x"), size)
		for _, encoding := range []string{"gzip", "zstd"} {
			var compressed []byte
			if encoding == "gzip" {
				var b bytes.Buffer
				w := gzip.NewWriter(&b)
				_, err := w.Write(payload)
				require.NoError(t, err)
				require.NoError(t, w.Close())
				compressed = b.Bytes()
			} else {
				w, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
				require.NoError(t, err)
				compressed = w.EncodeAll(payload, nil)
				w.Close()
			}
			req := newRequestWithBody(t, compressed, encoding)
			got, err := ReadRequestBodyWithPrealloc(req)
			if size > maxDecompressedBodySize {
				var limit *http.MaxBytesError
				require.True(t, errors.As(err, &limit), "%s: %v", encoding, err)
				require.Empty(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, len(payload), len(got))
			}
		}
	}
}
