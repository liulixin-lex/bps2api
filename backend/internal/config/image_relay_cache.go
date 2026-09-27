package config

import "fmt"

// ImageRelayCacheConfig bounds retained files and simultaneous public downloads.
// It is independent of request-body admission. Zero values keep prior defaults.
type ImageRelayCacheConfig struct {
	MaxEntries             int   `mapstructure:"max_entries"`
	MaxBytes               int64 `mapstructure:"max_bytes"`
	MaxDownloads           int   `mapstructure:"max_downloads"`
	DownloadTimeoutSeconds int   `mapstructure:"download_timeout_seconds"`
}

func (c ImageRelayCacheConfig) WithDefaults() ImageRelayCacheConfig {
	if c.MaxEntries == 0 {
		c.MaxEntries = 512
	}
	if c.MaxBytes == 0 {
		c.MaxBytes = 1 << 30
	}
	if c.MaxDownloads == 0 {
		c.MaxDownloads = 32
	}
	if c.DownloadTimeoutSeconds == 0 {
		c.DownloadTimeoutSeconds = 120
	}
	return c
}

func (c ImageRelayCacheConfig) Validate() error {
	c = c.WithDefaults()
	if c.MaxEntries < 1 || c.MaxEntries > 100000 {
		return fmt.Errorf("gateway.image_relay_cache.max_entries must be between 1 and 100000")
	}
	if c.MaxBytes < 1<<20 || c.MaxBytes > 64<<30 || uint64(c.MaxBytes) > uint64(^uint(0)>>1) {
		return fmt.Errorf("gateway.image_relay_cache.max_bytes must be between 1 MiB and 64 GiB and fit the platform integer size")
	}
	if c.MaxDownloads < 1 || c.MaxDownloads > 256 {
		return fmt.Errorf("gateway.image_relay_cache.max_downloads must be between 1 and 256")
	}
	if c.DownloadTimeoutSeconds < 1 || c.DownloadTimeoutSeconds > 600 {
		return fmt.Errorf("gateway.image_relay_cache.download_timeout_seconds must be between 1 and 600")
	}
	return nil
}
