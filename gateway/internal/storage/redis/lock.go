package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// releaseScript deletes the key only if this holder still owns it.
//
// A plain DEL would let a replica whose lock already expired delete the lock a different
// replica has since taken, which is the classic way a distributed lock stops being one.
const releaseScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`

// Lock is a cross-replica mutex for scheduled work (FL-07, T-19).
type Lock struct {
	rdb *goredis.Client
}

// NewLock wires the lock.
func NewLock(client *Client) *Lock { return &Lock{rdb: client.rdb} }

// Acquire takes the named lock for ttl.
//
// The returned release is always safe to call, including after the lock expired: it only
// deletes a key this holder still owns.
func (l *Lock) Acquire(ctx context.Context, name string, ttl time.Duration) (func(), bool, error) {
	token := uuid.NewString()

	ok, err := l.rdb.SetNX(ctx, name, token, ttl).Result()
	if err != nil {
		return nil, false, fmt.Errorf("acquire %s: %w", name, err)
	}
	if !ok {
		return func() {}, false, nil
	}

	return func() {
		// Detached from the caller's context: a cancelled run must still release, or
		// the lock would be held until its TTL for no reason.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = l.rdb.Eval(releaseCtx, releaseScript, []string{name}, token).Err()
	}, true, nil
}
