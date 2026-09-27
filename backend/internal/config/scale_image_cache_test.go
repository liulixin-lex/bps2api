package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScaleImageCacheConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, (ImageRelayCacheConfig{}).WithDefaults(), cfg.Gateway.ImageRelayCache)
	for _, bad := range []ImageRelayCacheConfig{
		{MaxEntries: -1}, {MaxEntries: 100001}, {MaxBytes: 1}, {MaxBytes: 65 << 30},
		{MaxDownloads: -1}, {MaxDownloads: 257},
		{DownloadTimeoutSeconds: -1}, {DownloadTimeoutSeconds: 601},
	} {
		require.Error(t, bad.Validate())
	}
	t.Setenv("GATEWAY_IMAGE_RELAY_CACHE_MAX_ENTRIES", "4096")
	t.Setenv("GATEWAY_IMAGE_RELAY_CACHE_MAX_BYTES", "8589934592")
	t.Setenv("GATEWAY_IMAGE_RELAY_CACHE_MAX_DOWNLOADS", "64")
	t.Setenv("GATEWAY_IMAGE_RELAY_CACHE_DOWNLOAD_TIMEOUT_SECONDS", "90")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, ImageRelayCacheConfig{MaxEntries: 4096, MaxBytes: 8 << 30, MaxDownloads: 64, DownloadTimeoutSeconds: 90}, cfg.Gateway.ImageRelayCache)
	t.Setenv("GATEWAY_IMAGE_RELAY_CACHE_MAX_DOWNLOADS", "257")
	_, err = Load()
	require.Error(t, err)
}
