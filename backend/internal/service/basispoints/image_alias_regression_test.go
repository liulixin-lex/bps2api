package basispoints

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageAliasUnsupportedContentIsNeverSilentlyLowered(t *testing.T) {
	for _, kind := range []string{"input_file", "file", "document", "input_audio", "audio", "provider_unknown"} {
		t.Run(kind, func(t *testing.T) {
			part := object{"type": kind, "text": "private text", "image_url": "https://images.example/photo.png"}
			before, err := json.Marshal(part)
			require.NoError(t, err)
			normalizeContentPart(part)
			after, err := json.Marshal(part)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
			source := testSource()
			source["input"] = []any{object{"role": "user", "content": []any{part}}}
			raw, err := json.Marshal(source)
			require.NoError(t, err)
			_, _, err = Prepare(raw, "scope", nil)
			require.Error(t, err)
		})
	}
}

func TestImageAliasInlineDetectionRelayAndWirePreserveOriginal(t *testing.T) {
	data := relayTestPNG(t)
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	for name, image := range map[string]object{
		"provider image":   {"type": "provider_image", "image_url": dataURL, "detail": "original"},
		"source URL":       {"type": "output_image", "source": object{"type": "url", "url": dataURL, "detail": "original"}},
		"nested image URL": {"type": "computer_screenshot", "source": object{"image_url": dataURL}, "detail": "original"},
		"top URL":          {"type": "screenshot", "url": dataURL, "detail": "original"},
		"image":            {"type": "image", "source": dataURL, "detail": "original"},
	} {
		t.Run(name, func(t *testing.T) {
			source := testSource()
			source["input"] = []any{object{"role": "user", "content": []any{image}}}
			raw, err := json.Marshal(source)
			require.NoError(t, err)
			require.True(t, RequestHasInlineImages(raw), "alias must receive inline image admission")
			r, err := NewImageRelay("https://images.example", t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, r.Close()) })
			out, err := r.Rewrite(raw, "scope")
			require.NoError(t, err)
			var rewritten object
			require.NoError(t, decode(out, &rewritten))
			items := mustTestValue[[]any](t, rewritten["input"])
			item := mustTestValue[object](t, items[0])
			parts := mustTestValue[[]any](t, item["content"])
			part := mustTestValue[object](t, parts[0])
			require.Equal(t, "original", part["detail"])
			require.Len(t, part, 3)
			url := mustTestValue[string](t, part["image_url"])
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, data, w.Body.Bytes(), "relay must preserve decoded image bytes")
			wire, _, err := Prepare(out, "scope", nil)
			require.NoError(t, err)
			var prepared object
			require.NoError(t, decode(wire, &prepared))
			wireItems := mustTestValue[[]any](t, prepared["input"])
			wireItem := mustTestValue[object](t, wireItems[len(wireItems)-1])
			wireParts := mustTestValue[[]any](t, wireItem["content"])
			require.True(t, reflect.DeepEqual(part, wireParts[0]), "wire must preserve original and relay URL")
		})
	}
}

func TestImageAliasTopAndNestedMapDetailsNeverPanic(t *testing.T) {
	part := object{"type": "provider_image", "detail": object{"value": "original"}, "source": object{"url": "https://images.example/photo.png", "detail": object{"value": "original"}}}
	require.NotPanics(t, func() { normalizeContentPart(part) })
	require.Equal(t, "provider_image", part["type"])
}

func TestImageAliasCanonicalConflictsAreRejected(t *testing.T) {
	for _, image := range []object{
		{"type": "input_image", "image_url": "https://images.example/a.png", "url": "https://images.example/b.png"},
		{"type": "input_image", "image_url": "https://images.example/a.png", "source": object{"url": "https://images.example/b.png"}},
		{"type": "input_image", "image_url": "https://images.example/a.png", "text": "not an image-only payload"},
	} {
		source := testSource()
		source["input"] = []any{object{"role": "user", "content": []any{image}}}
		raw, err := json.Marshal(source)
		require.NoError(t, err)
		_, _, err = Prepare(raw, "scope", nil)
		require.Error(t, err)
	}
}
