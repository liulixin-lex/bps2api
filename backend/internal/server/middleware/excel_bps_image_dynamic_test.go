package middleware

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type liveBPSImageSettings struct {
	mu    sync.Mutex
	value service.ExcelBPSImageRelaySettings
}

func (s *liveBPSImageSettings) GetExcelBPSImageRelaySettings(context.Context) (service.ExcelBPSImageRelaySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value, nil
}
func (s *liveBPSImageSettings) set(v service.ExcelBPSImageRelaySettings) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.value = v
}

func TestExcelBPSImageAdmissionLiveSettingsRespectDeploymentCeiling(t *testing.T) {
	initMiddlewareTestLogger(t)
	settings := &liveBPSImageSettings{value: service.ExcelBPSImageRelaySettings{Enabled: true, BodyLimitMiB: 1, BudgetMiB: 512, MaxRequests: 1}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(string(ContextKeyAPIKey), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}})
		c.Next()
	})
	r.Use(ExcelBPSImageAdmission(settings, 64<<20, config.ImageRelayAdmissionConfig{MaxConcurrentRequests: 2, ProcessingBudgetBytes: 16 << 20}))
	entered, release := make(chan struct{}, 3), make(chan struct{})
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	r.POST("/v1/responses", func(c *gin.Context) {
		if c.GetHeader("Hold") == "1" {
			entered <- struct{}{}
			<-release
		}
		c.Status(200)
	})
	request := func(hold bool) int {
		q := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}]}`))
		if hold {
			q.Header.Set("Hold", "1")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		return w.Code
	}
	hold := func() {
		wg.Add(1)
		go func() { defer wg.Done(); request(true) }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("request did not enter")
		}
	}
	hold()
	require.Equal(t, 503, request(false), "saved one-slot limit applies without restart")
	settings.set(service.ExcelBPSImageRelaySettings{Enabled: true, BodyLimitMiB: 1, BudgetMiB: 2048, MaxRequests: 512})
	hold()
	require.Equal(t, 503, request(false), "UI settings cannot exceed deployment memory/concurrency ceilings")
	settings.set(service.ExcelBPSImageRelaySettings{Enabled: true, BodyLimitMiB: 1, BudgetMiB: 512, MaxRequests: 1})
	require.Equal(t, 503, request(false), "lowering capacity must account for existing requests")
}

func TestExcelBPSImageAdmissionBudgetDoesNotAutoExpand(t *testing.T) {
	b := &bpsImageAdmissionBudget{}
	var leases []*bpsImageReservation
	for i := 0; i < 64; i++ {
		lease, ok := b.reserveConfigured(8<<20, 512<<20, 512)
		require.True(t, ok)
		leases = append(leases, lease)
	}
	_, ok := b.reserveConfigured(8<<20, 512<<20, 512)
	require.False(t, ok, "512 slots must not silently expand 512 MiB to 4 GiB")
	_, ok = b.reserveConfigured(8<<20, 8<<20, 1)
	require.False(t, ok)
	require.True(t, leases[0].resize(4<<20), "shrinking a lease is safe even after lowering the budget below usage")
	for _, lease := range leases {
		lease.release()
	}
	used, active := b.snapshot()
	require.Zero(t, used)
	require.Zero(t, active)
}

func TestBPSImageRequestNeedsRelay(t *testing.T) {
	require.False(t, bpsImageRequestNeedsRelay([]byte(`{"input":"hello"}`)))
	require.False(t, bpsImageRequestNeedsRelay([]byte(`{"input":[{"type":"input_image","image_url":"https://example.com/image.png"}]}`)))
	require.True(t, bpsImageRequestNeedsRelay([]byte(`{"input":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}`)))
}
