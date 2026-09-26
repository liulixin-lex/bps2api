package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type opsQueueDepthCache struct {
	ConcurrencyCache
	depth int
	err   error
}

func (c *opsQueueDepthCache) GetConcurrencyQueueDepth(ctx context.Context) (int, error) {
	if _, ok := ctx.Deadline(); !ok {
		return 0, errors.New("sample must have a deadline")
	}
	return c.depth, c.err
}

func TestCollectConcurrencyQueueDepthIncludesUserQueuesWithoutAccounts(t *testing.T) {
	cache := &opsQueueDepthCache{depth: 5}
	collector := &OpsMetricsCollector{concurrencyService: NewConcurrencyService(cache)}
	depth := collector.collectConcurrencyQueueDepth(context.Background())
	require.NotNil(t, depth)
	require.Equal(t, 5, *depth)
	cache.err = errors.New("Redis unavailable")
	require.Nil(t, collector.collectConcurrencyQueueDepth(context.Background()), "failed samples must not report zero")
}
