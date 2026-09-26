package repository

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestConcurrencyQueueDepthIncludesUsersAndAccounts(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 60)
	sampler, ok := cache.(service.ConcurrencyQueueDepthCache)
	require.True(t, ok)
	ctx := context.Background()
	for range 3 {
		acquired, err := cache.IncrementWaitCount(ctx, 7, 10)
		require.NoError(t, err)
		require.True(t, acquired)
	}
	for range 2 {
		acquired, err := cache.IncrementAccountWaitCount(ctx, 7, 10)
		require.NoError(t, err)
		require.True(t, acquired)
	}
	before := server.TTL(waitQueueKey(7))
	depth, err := sampler.GetConcurrencyQueueDepth(ctx)
	require.NoError(t, err)
	require.Equal(t, 5, depth)
	require.Equal(t, before, server.TTL(waitQueueKey(7)), "sampling must not renew queue leases")
	require.NoError(t, cache.DecrementWaitCount(ctx, 7))
	depth, err = sampler.GetConcurrencyQueueDepth(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, depth)
	server.FastForward(time.Minute + time.Second)
	depth, err = sampler.GetConcurrencyQueueDepth(ctx)
	require.NoError(t, err)
	require.Zero(t, depth, "expired queue keys must not remain in the sample")
}

func TestConcurrencyQueueDepthPaginationAndOverflow(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 60).(*concurrencyCache)
	for id := int64(1); id <= 1201; id++ {
		_, err := server.ZAdd(userActiveIndexKey, 1, fmt.Sprint(id))
		require.NoError(t, err)
		require.NoError(t, server.Set(waitQueueKey(id), "1"))
	}
	_, err := server.ZAdd(userActiveIndexKey, 1, "invalid")
	require.NoError(t, err)
	_, err = server.ZAdd(userActiveIndexKey, 1, "0001")
	require.NoError(t, err)
	depth, err := cache.GetConcurrencyQueueDepth(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1201, depth, "scan pages and alternate ID spellings must not double count")
	require.NoError(t, server.Set(waitQueueKey(1), fmt.Sprint(int64(math.MaxInt64))))
	depth, err = cache.GetConcurrencyQueueDepth(context.Background())
	require.NoError(t, err)
	require.Equal(t, int(^uint(0)>>1), depth)
}

func TestConcurrencyQueueDepthRejectsUnavailableOrCorruptSample(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewConcurrencyCache(client, 15, 60).(*concurrencyCache)
	_, err := server.ZAdd(userActiveIndexKey, 1, "1")
	require.NoError(t, err)
	require.NoError(t, server.Set(waitQueueKey(1), "not-a-number"))
	_, err = cache.GetConcurrencyQueueDepth(context.Background())
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cache.GetConcurrencyQueueDepth(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
