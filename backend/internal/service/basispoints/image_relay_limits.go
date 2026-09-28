package basispoints

import (
	"fmt"
	"strings"
)

// ImageRelayLimits bounds decoded images and per-process temporary storage.
// A saved change applies to new conversions; existing links keep their expiry
// until resubmission. Lowering storage limits never evicts live images.
type ImageRelayLimits struct {
	MaxImageMiB    int
	MaxImages      int
	MaxTotalMiB    int
	StorageMiB     int
	StorageEntries int
	TTLMinutes     int
}

func DefaultImageRelayLimits() ImageRelayLimits {
	return ImageRelayLimits{
		MaxImageMiB:    imageRelayMaxImageBytes >> 20,
		MaxImages:      imageRelayMaxRequestImages,
		MaxTotalMiB:    (imageRelayMaxRequestBytes + (1 << 20) - 1) >> 20,
		StorageMiB:     imageRelayMaxBytes >> 20,
		StorageEntries: imageRelayMaxEntries,
		TTLMinutes:     30,
	}
}

func (l ImageRelayLimits) Validate() error {
	if l.MaxImageMiB < 1 || l.MaxImageMiB > 128 {
		return fmt.Errorf("image relay max_image_mib must be 1-128")
	}
	if l.MaxImages < 1 || l.MaxImages > 4096 {
		return fmt.Errorf("image relay max_images must be 1-4096")
	}
	if l.MaxTotalMiB < 1 || l.MaxTotalMiB > 128 {
		return fmt.Errorf("image relay max_total_mib must be 1-128")
	}
	if l.StorageMiB < 1 || l.StorageMiB > 16384 {
		return fmt.Errorf("image relay storage_mib must be 1-16384")
	}
	if l.StorageEntries < 1 || l.StorageEntries > 65536 {
		return fmt.Errorf("image relay storage_entries must be 1-65536")
	}
	if l.TTLMinutes < 1 || l.TTLMinutes > 1440 {
		return fmt.Errorf("image relay ttl_minutes must be 1-1440")
	}
	if l.MaxTotalMiB < l.MaxImageMiB {
		return fmt.Errorf("image relay request image size limit must cover a single image")
	}
	if l.StorageMiB < l.MaxTotalMiB {
		return fmt.Errorf("image relay storage must cover the request image size limit")
	}
	if l.StorageEntries < l.MaxImages {
		return fmt.Errorf("image relay storage entries must cover the request image count")
	}
	return nil
}

// Preserve the pre-integration exact 50 MB default instead of rounding it down.
func (l ImageRelayLimits) maxRequestBytes() int64 {
	if l.MaxTotalMiB == DefaultImageRelayLimits().MaxTotalMiB {
		return imageRelayMaxRequestBytes
	}
	return int64(l.MaxTotalMiB) << 20
}

// Configure updates origin and limits together without invalidating live links.
func (r *ImageRelay) Configure(baseURL string, limits ImageRelayLimits) error {
	if err := ValidateImageRelayOrigin(baseURL); err != nil {
		return err
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrImageRelayStorage
	}
	if int64(limits.StorageMiB)<<20 > int64(r.maxBytes) || limits.StorageEntries > r.maxEntries {
		return fmt.Errorf("image relay storage limits exceed deployment hard limits")
	}
	r.baseURL = strings.TrimRight(baseURL, "/")
	r.limits = limits
	return nil
}
