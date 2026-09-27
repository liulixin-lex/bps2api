package config

import "fmt"

// ExcelBPSTimeoutConfig is opt-in: reasoning streams may legitimately run for
// minutes. These limits are independent of ingress/body-reading deadlines.
type ExcelBPSTimeoutConfig struct {
	FirstOutputSeconds int `mapstructure:"first_output_seconds"`
	IdleSeconds        int `mapstructure:"idle_seconds"`
	TotalSeconds       int `mapstructure:"total_seconds"`
}

func (c ExcelBPSTimeoutConfig) Validate() error {
	for name, value := range map[string]int{"first_output_seconds": c.FirstOutputSeconds, "idle_seconds": c.IdleSeconds, "total_seconds": c.TotalSeconds} {
		if value < 0 || value > 7200 {
			return fmt.Errorf("gateway.excel_bps_timeouts.%s must be between 0 and 7200", name)
		}
	}
	return nil
}

// ImageRelayAdmissionConfig bounds process-local ingress work separately from
// retained request bodies. Zero values select safe defaults, not unlimited work.
type ImageRelayAdmissionConfig struct {
	DecodeMaxConcurrent    int   `mapstructure:"decode_max_concurrent"`
	DecodeWaitMilliseconds int   `mapstructure:"decode_wait_milliseconds"`
	DecodeBudgetBytes      int64 `mapstructure:"decode_budget_bytes"`
	ProcessingBudgetBytes  int64 `mapstructure:"processing_budget_bytes"`
	MaxConcurrentRequests  int   `mapstructure:"max_concurrent_requests"`
	BodyReadTimeoutSeconds int   `mapstructure:"body_read_timeout_seconds"`
}

// DecodeReservationBytes covers bounded wire/output buffers, their assembly
// copies and decoder workspace. It is a reservation, not an RSS measurement.
const ImageRelayDecodeReservationBytes int64 = 512 << 20

func (c ImageRelayAdmissionConfig) WithDefaults() ImageRelayAdmissionConfig {
	if c.DecodeMaxConcurrent == 0 {
		c.DecodeMaxConcurrent = 4
	}
	if c.DecodeWaitMilliseconds == 0 {
		c.DecodeWaitMilliseconds = 250
	}
	if c.DecodeBudgetBytes == 0 {
		c.DecodeBudgetBytes = ImageRelayDecodeReservationBytes
	}
	if c.ProcessingBudgetBytes == 0 {
		c.ProcessingBudgetBytes = 1 << 30
	}
	if c.MaxConcurrentRequests == 0 {
		c.MaxConcurrentRequests = 128
	}
	if c.BodyReadTimeoutSeconds == 0 {
		c.BodyReadTimeoutSeconds = 60
	}
	return c
}

func (c ImageRelayAdmissionConfig) Validate() error {
	c = c.WithDefaults()
	if c.DecodeMaxConcurrent < 1 || c.DecodeMaxConcurrent > 32 {
		return fmt.Errorf("gateway.image_relay_admission.decode_max_concurrent must be between 1 and 32")
	}
	if c.DecodeWaitMilliseconds < 1 || c.DecodeWaitMilliseconds > 2000 {
		return fmt.Errorf("gateway.image_relay_admission.decode_wait_milliseconds must be between 1 and 2000")
	}
	if c.DecodeBudgetBytes < ImageRelayDecodeReservationBytes || c.DecodeBudgetBytes > 32*ImageRelayDecodeReservationBytes {
		return fmt.Errorf("gateway.image_relay_admission.decode_budget_bytes must be between 512 MiB and 16 GiB")
	}
	if c.ProcessingBudgetBytes < 8<<20 || c.ProcessingBudgetBytes > 16<<30 {
		return fmt.Errorf("gateway.image_relay_admission.processing_budget_bytes must be between 8 MiB and 16 GiB")
	}
	if c.MaxConcurrentRequests < 1 || c.MaxConcurrentRequests > 4096 {
		return fmt.Errorf("gateway.image_relay_admission.max_concurrent_requests must be between 1 and 4096")
	}
	if c.BodyReadTimeoutSeconds < 1 || c.BodyReadTimeoutSeconds > 600 {
		return fmt.Errorf("gateway.image_relay_admission.body_read_timeout_seconds must be between 1 and 600")
	}
	return nil
}
