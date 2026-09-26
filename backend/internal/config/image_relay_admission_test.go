package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestImageRelayAdmissionConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, (ImageRelayAdmissionConfig{}).WithDefaults(), cfg.Gateway.ImageRelayAdmission)
	for _, c := range []ImageRelayAdmissionConfig{
		{DecodeMaxConcurrent: -1}, {DecodeBudgetBytes: 1}, {ProcessingBudgetBytes: 1},
		{MaxConcurrentRequests: -1}, {BodyReadTimeoutSeconds: -1}, {BodyReadTimeoutSeconds: 601},
		{DecodeWaitMilliseconds: -1}, {DecodeWaitMilliseconds: 2001},
	} {
		require.Error(t, c.Validate())
	}
	t.Setenv("GATEWAY_IMAGE_RELAY_ADMISSION_PROCESSING_BUDGET_BYTES", "1073741824")
	t.Setenv("GATEWAY_IMAGE_RELAY_ADMISSION_DECODE_MAX_CONCURRENT", "2")
	t.Setenv("GATEWAY_IMAGE_RELAY_ADMISSION_DECODE_WAIT_MILLISECONDS", "500")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, int64(1<<30), cfg.Gateway.ImageRelayAdmission.ProcessingBudgetBytes)
	require.Equal(t, 2, cfg.Gateway.ImageRelayAdmission.DecodeMaxConcurrent)
	require.Equal(t, 500, cfg.Gateway.ImageRelayAdmission.DecodeWaitMilliseconds)
}

func TestExcelBPSTimeoutsConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.Equal(t, ExcelBPSTimeoutConfig{}, cfg.Gateway.ExcelBPSTimeouts)
	t.Setenv("GATEWAY_EXCEL_BPS_TIMEOUTS_IDLE_SECONDS", "300")
	cfg, err = Load()
	require.NoError(t, err)
	require.Equal(t, 300, cfg.Gateway.ExcelBPSTimeouts.IdleSeconds)
	for _, c := range []ExcelBPSTimeoutConfig{{IdleSeconds: -1}, {FirstOutputSeconds: 7201}, {TotalSeconds: -1}} {
		require.Error(t, c.Validate())
	}
}
