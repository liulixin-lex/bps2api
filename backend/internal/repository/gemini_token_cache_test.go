//go:build unit

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGeminiTokenCache_MissExpiryAndFailure(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewGeminiTokenCache(rdb)
	ctx := context.Background()
	token, err := cache.GetAccessToken(ctx, "missing")
	require.NoError(t, err)
	require.Empty(t, token)
	require.NoError(t, cache.SetAccessToken(ctx, "expires", "token", time.Second))
	server.FastForward(2 * time.Second)
	token, err = cache.GetAccessToken(ctx, "expires")
	require.NoError(t, err)
	require.Empty(t, token)
	server.SetError("ERR backend unavailable")
	_, err = cache.GetAccessToken(ctx, "missing")
	require.ErrorContains(t, err, "backend unavailable")
}

func TestGeminiTokenCache_DeleteAccessToken_RedisError(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		_ = rdb.Close()
	})

	cache := NewGeminiTokenCache(rdb)
	err := cache.DeleteAccessToken(context.Background(), "broken")
	require.Error(t, err)
}
