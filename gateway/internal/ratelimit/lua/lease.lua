-- Concurrency lease, one sorted set per tenant. Members are lease ids scored by their
-- expiry, so an expired lease is evicted by the next caller rather than by a sweeper.
--
-- KEYS[1] conc:{tenant_id}
-- ARGV[1] limit       max concurrent streams (= plan max_concurrent_streams)
-- ARGV[2] now_ms
-- ARGV[3] ttl_ms      lease lifetime, renewed while the stream runs
-- ARGV[4] lease_id
--
-- Returns {acquired, active}

local limit = tonumber(ARGV[1])
local now_ms = tonumber(ARGV[2])
local ttl_ms = tonumber(ARGV[3])
local lease_id = ARGV[4]

-- Drop leases whose holder died: a crashed gateway must not hold capacity forever
-- (US-06 acceptance criterion 3).
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms)

local active = redis.call('ZCARD', KEYS[1])
if active >= limit then
  return {0, active}
end

redis.call('ZADD', KEYS[1], now_ms + ttl_ms, lease_id)
-- Bound the key itself, so a tenant that stops calling leaves nothing behind.
redis.call('PEXPIRE', KEYS[1], ttl_ms * 2)

return {1, active + 1}
