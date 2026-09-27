package basispoints

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHTTPSImagesPreserveURLsAndText(t *testing.T) {
	for _, detail := range []string{"", "auto", "low", "high", "original"} {
		image := object{"type": "input_image", "image_url": "https://images.example/photo.png?signature=unchanged%2Fvalue&expires=123"}
		if detail != "" {
			image["detail"] = detail
		}
		item := object{"type": "message", "role": "user", "content": []any{object{"type": "input_text", "text": "Describe this image."}, image}}
		source := testSource()
		source["input"] = []any{item}
		wire, _ := mustPrepare(t, source, "scope", nil)
		items := mustTestValue[[]any](t, wire["input"])
		if !reflect.DeepEqual(items[len(items)-1], item) {
			t.Fatal("image URL, detail or neighboring text was changed")
		}
	}
}

func TestCompatImageProviderAliasesNormalizeToHTTPSInputImage(t *testing.T) {
	for name, image := range map[string]object{
		"provider type with image_url": {"type": "provider_image", "image_url": "https://images.example/provider.png", "detail": "low"},
		"output image source":          {"type": "output_image", "source": object{"url": "https://images.example/output.png", "detail": "high"}},
		"screenshot url":               {"type": "screenshot", "url": "https://images.example/screenshot.png"},
	} {
		t.Run(name, func(t *testing.T) {
			source := testSource()
			source["input"] = []any{object{"role": "user", "content": []any{image}}}
			wire, _ := mustPrepare(t, source, "scope", nil)
			items := mustTestValue[[]any](t, wire["input"])
			message := mustTestValue[object](t, items[len(items)-1])
			content := mustTestValue[[]any](t, message["content"])
			part := mustTestValue[object](t, content[0])
			if part["type"] != "input_image" || part["image_url"] == "" {
				t.Fatalf("alias was not normalized: %#v", part)
			}
		})
	}
}

func TestUnsupportedImageFormsReturnActionableErrors(t *testing.T) {
	for name, image := range map[string]object{
		"base64":               {"image_url": "data:image/png;base64,PRIVATE_IMAGE_BYTES"},
		"http":                 {"image_url": "http://images.example/photo.png"},
		"relative":             {"image_url": "/photo.png"},
		"local file":           {"image_url": "file:///private/photo.png"},
		"missing host":         {"image_url": "https:///photo.png"},
		"credentials":          {"image_url": "https://private-secret:password@images.example/photo.png"},
		"ambiguous URL object": {"image_url": object{"url": "https://images.example/photo.png", "file_id": "file-private"}},
		"file ID":              {"file_id": "file-private"},
		"mixed file ID":        {"image_url": "https://images.example/photo.png", "file_id": "file-private"},
		"unknown detail":       {"image_url": "https://images.example/photo.png", "detail": "ultra"},
		"numeric detail":       {"image_url": "https://images.example/photo.png", "detail": 123},
		"object detail":        {"image_url": "https://images.example/photo.png", "detail": object{"value": "original"}},
	} {
		t.Run(name, func(t *testing.T) {
			image["type"] = "input_image"
			source := testSource()
			source["input"] = []any{object{"role": "user", "content": []any{image}}}
			raw, _ := json.Marshal(source)
			_, _, err := Prepare(raw, "", nil)
			if err == nil {
				t.Fatal("unsupported image silently forwarded")
			}
			if strings.Contains(err.Error(), "PRIVATE_IMAGE_BYTES") || strings.Contains(err.Error(), "private-secret") {
				t.Fatal("image data or credentials leaked into the error")
			}
			if name == "base64" && (!strings.Contains(err.Error(), "HTTPS image URL") || !strings.Contains(err.Error(), "disable Basispoints")) {
				t.Fatalf("base64 rejection lacks a remedy: %v", err)
			}
		})
	}
}

func TestCompatImageAliasDoesNotMutateRelayOnlyUnsupportedFields(t *testing.T) {
	part := object{"type": "provider_image", "source": object{"url": "https://images.example/photo.png", "detail": "original"}}
	normalizeContentPart(part)
	if part["type"] != "input_image" || part["image_url"] != "https://images.example/photo.png" {
		t.Fatalf("alias was not normalized: %#v", part)
	}
	if _, ok := part["source"]; ok {
		t.Fatal("source alias leaked to BPS wire")
	}
	if _, ok := part["url"]; ok {
		t.Fatal("url alias leaked to BPS wire")
	}
	if part["detail"] != "original" {
		t.Fatalf("detail changed: %#v", part["detail"])
	}
}

func TestCompatImageAliasRejectsAmbiguousMapsAndUnknownPayload(t *testing.T) {
	for name, part := range map[string]object{
		"file id":                {"type": "provider_image", "image_url": "https://images.example/photo.png", "file_id": "file-private"},
		"binary source":          {"type": "provider_image", "source": object{"data": "private-bytes"}},
		"conflicting references": {"type": "provider_image", "image_url": "https://images.example/a.png", "url": "https://images.example/b.png"},
		"conflicting details":    {"type": "provider_image", "detail": "low", "source": object{"url": "https://images.example/a.png", "detail": "high"}},
	} {
		t.Run(name, func(t *testing.T) {
			normalizeContentPart(part)
			if text(part["type"]) == "input_image" {
				t.Fatalf("ambiguous payload was normalized: %#v", part)
			}
		})
	}
}

func TestCompatImageAliasDetailMapDoesNotPanic(t *testing.T) {
	part := object{"type": "provider_image", "image_url": "https://images.example/photo.png", "detail": object{"value": "original"}}
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("image alias normalization panicked: %v", recovered)
		}
	}()
	normalizeContentPart(part)
	if text(part["type"]) == "input_image" {
		t.Fatalf("non-scalar detail was normalized: %#v", part)
	}
}
