package basispoints

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamMergeMediaOptInAndDefaultHostedCompatibility(t *testing.T) {
	source := testSource()
	source["input"] = []any{object{"role": "user", "content": []any{object{"type": "input_image", "file_id": "file-explicit", "detail": "original"}}}}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	_, _, err = Prepare(raw, "scope", nil)
	require.ErrorContains(t, err, "explicit native attachment mode")
	_, bridge, err := PrepareWithCatalog(raw, "scope", nil, new(CatalogCache), PrepareOptions{NativeAttachments: true})
	require.NoError(t, err)
	_, _, err = bridge.Reprepare(raw)
	require.NoError(t, err)
	_, _, err = Prepare(raw, "scope", nil)
	require.Error(t, err, "a previous native request cannot enable the default path")
	source = testSource()
	source["tools"] = []any{object{"type": "web_search"}}
	raw, err = json.Marshal(source)
	require.NoError(t, err)
	_, bridge, err = Prepare(raw, "scope", nil)
	require.NoError(t, err)
	require.Len(t, bridge.Warnings, 1)
	_, bridge, err = Prepare(raw, "scope", nil, PrepareOptions{OmitUnsupportedTools: true})
	require.NoError(t, err)
	require.Len(t, bridge.Warnings, 1)
	source = testSource()
	source["input"] = []any{object{"role": "user", "content": []any{object{"type": "encrypted_content", "encrypted_content": "private-ciphertext"}}}}
	raw, err = json.Marshal(source)
	require.NoError(t, err)
	_, _, err = Prepare(raw, "scope", nil)
	require.ErrorContains(t, err, "encrypted_content")
	require.NotContains(t, err.Error(), "private-ciphertext")
}

func TestUpstreamMergeNativeAliasesRetainOriginalAndOmission(t *testing.T) {
	_, data := nativeTestURL(t)
	binary := "data:application/octet-stream;base64," + base64.StdEncoding.EncodeToString(data)
	source := testSource()
	source["input"] = []any{object{"type": "agent_message", "author": "worker", "content": []any{
		object{"type": "provider_image", "source": object{"url": binary, "detail": "original"}},
		object{"type": "computer_screenshot", "image_url": binary},
	}}}
	raw, err := json.Marshal(source)
	require.NoError(t, err)
	plan, err := PrepareNativeImages(raw)
	require.NoError(t, err)
	_, bridge, err := plan.PrepareWithCatalog("scope", nil, nil)
	require.NoError(t, err)
	uploads := 0
	uploaded, err := plan.Upload(context.Background(), new(AttachmentCache), "scope", func(_ context.Context, img InlineAttachment) (string, error) {
		uploads++
		require.Equal(t, "image/png", img.MIME)
		return "file-alias", nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, uploads)
	wire, _, err := bridge.Reprepare(uploaded)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(wire), "\"detail\":\"original\""))
	require.NotContains(t, string(wire), "\"detail\":\"auto\"")
	require.Equal(t, 2, strings.Count(string(wire), "file-alias"))
	require.Contains(t, string(raw), binary, "caller input remains unchanged")
}

func TestUpstreamMergeRelayDeploymentBoundsAndCachedSize(t *testing.T) {
	require.Equal(t, 500, DefaultImageRelayLimits().MaxImages)
	require.Equal(t, int64(50_000_000), DefaultImageRelayLimits().maxRequestBytes())
	relay, err := NewImageRelay("https://images.example", t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, relay.Close()) })
	limits := DefaultImageRelayLimits()
	limits.StorageMiB++
	require.ErrorContains(t, relay.Configure(relay.baseURL, limits), "deployment hard limits")
	limits = DefaultImageRelayLimits()
	limits.StorageEntries++
	require.Error(t, relay.Configure(relay.baseURL, limits))
	padded := make([]byte, (1<<20)+1)
	copy(padded, relayTestPNG(t))
	raw := relayTestRequest(t, padded)
	_, err = relay.Rewrite(raw, "scope")
	require.NoError(t, err)
	limits = DefaultImageRelayLimits()
	limits.MaxImageMiB = 1
	require.NoError(t, relay.Configure(relay.baseURL, limits))
	_, err = relay.Rewrite(raw, "scope")
	require.ErrorContains(t, err, "configured 1 MiB")
	require.Zero(t, relay.reservedBytes)
	require.Zero(t, relay.reservedEntries)
}

func TestPreOutputRepairStopsAfterVisibleTextOrMetadata(t *testing.T) {
	for _, prefix := range []string{
		sse(object{"type": "response.output_text.delta", "delta": "already visible"}),
		sse(object{"type": "response.in_progress", "metadata": strings.Repeat("x", 256<<10)}),
	} {
		source := testSource()
		source["tools"] = []any{object{"type": "function", "name": "known"}}
		_, bridge := mustPrepare(t, source, "scope", nil)
		invalid := nativeCall(object{"name": "missing", "arguments": object{}})
		wire := prefix + sse(object{"type": "response.completed", "response": object{"status": "completed", "output": []any{invalid}}})
		calls := 0
		reader := bridge.StreamWithPreOutputRepairs(context.Background(), io.NopCloser(strings.NewReader(wire)), func(context.Context, map[string]any, error) (map[string]any, error) { calls++; return nil, nil }, func(context.Context) (io.ReadCloser, error) { calls++; return io.NopCloser(bytes.NewReader(nil)), nil })
		out, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NoError(t, reader.Close())
		require.Zero(t, calls)
		require.Contains(t, string(out), "response.failed")
		require.NotContains(t, string(out), "response.function_call_arguments.delta")
	}
}

func TestPreOutputUnknownRepairWorksBeforeOutput(t *testing.T) {
	source := testSource()
	source["tools"] = []any{object{"type": "function", "name": "known"}}
	_, bridge := mustPrepare(t, source, "scope", nil)
	invalid := nativeCall(object{"name": "missing", "arguments": object{}})
	wire := sse(object{"type": "response.completed", "response": object{"status": "completed", "output": []any{invalid}}})
	valid := nativeCall(object{"name": "known", "arguments": object{}})
	fixed := sse(object{"type": "response.completed", "response": object{"status": "completed", "output": []any{valid}}})
	calls := 0
	reader := bridge.StreamWithPreOutputRepairs(context.Background(), io.NopCloser(strings.NewReader(wire)), nil, func(context.Context) (io.ReadCloser, error) {
		calls++
		return io.NopCloser(strings.NewReader(fixed)), nil
	})
	out, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, 1, calls)
	require.Contains(t, string(out), "response.completed")
	require.NotContains(t, string(out), "response.failed")
}
