-- Extend a held lease. Returns 0 when the lease is already gone, which tells the caller
-- its stream has been reclaimed and it must stop rather than keep consuming capacity.
--
-- KEYS[1] conc:{tenant_id}
-- ARGV[1] now_ms
-- ARGV[2] ttl_ms
-- ARGV[3] lease_id

local now_ms = tonumber(ARGV[1])
local ttl_ms = tonumber(ARGV[2])
local lease_id = ARGV[3]

if redis.call('ZSCORE', KEYS[1], lease_id) == false then
  return 0
end

redis.call('ZADD', KEYS[1], now_ms + ttl_ms, lease_id)
redis.call('PEXPIRE', KEYS[1], ttl_ms * 2)
return 1
