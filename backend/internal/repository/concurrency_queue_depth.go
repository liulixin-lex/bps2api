package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/redis/go-redis/v9"
)

// GetConcurrencyQueueDepth is a best-effort instantaneous sample. Read queue
// keys through the existing active indexes, without scanning the Redis keyspace
// or mutating slot/TTL state. The collector supplies a short context deadline.
func (c *concurrencyCache) GetConcurrencyQueueDepth(ctx context.Context) (int, error) {
	var total int64
	maxInt := int64(^uint(0) >> 1)
	for _, spec := range []slotIndexSpec{userSlotIndex, accountSlotIndex} {
		seen := make(map[int64]struct{})
		var cursor uint64
		for {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			members, next, err := c.rdb.ZScan(ctx, spec.indexKey, cursor, "*", activeIndexPipelineChunkSize).Result()
			if err != nil {
				return 0, fmt.Errorf("scan queue index %s: %w", spec.indexKey, err)
			}
			// ZSCAN COUNT is a hint, so also bound each pipeline explicitly.
			for start := 0; start < len(members); start += 2 * activeIndexPipelineChunkSize {
				end := min(start+2*activeIndexPipelineChunkSize, len(members))
				pipe := c.rdb.Pipeline()
				commands := make([]*redis.StringCmd, 0, (end-start)/2)
				for i := start; i+1 < end; i += 2 {
					id, err := strconv.ParseInt(members[i], 10, 64)
					if err != nil || id <= 0 {
						continue
					}
					if _, duplicate := seen[id]; duplicate {
						continue
					}
					seen[id] = struct{}{}
					commands = append(commands, pipe.Get(ctx, spec.waitKey(id)))
				}
				if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
					return 0, fmt.Errorf("read queue counts: %w", err)
				}
				for _, cmd := range commands {
					count, err := cmd.Int64()
					if errors.Is(err, redis.Nil) {
						continue
					}
					if err != nil {
						return 0, fmt.Errorf("invalid queue count: %w", err)
					}
					if count > 0 {
						total += min(count, maxInt-total)
					}
				}
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	return int(total), nil
}
