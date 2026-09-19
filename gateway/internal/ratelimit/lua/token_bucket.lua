-- Token bucket, one key per API key. Atomic by being a single script: refill, test and
-- consume cannot interleave with another request's (US-06 acceptance criterion 4).
--
-- KEYS[1] rl:{key_id}
-- ARGV[1] capacity        tokens the bucket holds  (= plan req_per_minute)
-- ARGV[2] refill_per_sec  tokens added per second  (= capacity / 60)
-- ARGV[3] now_ms          caller's clock, so the script stays deterministic
-- ARGV[4] cost            tokens this request consumes
--
-- Returns {allowed, remaining, retry_after_ms}

local capacity = tonumber(ARGV[1])
local refill_per_sec = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local cost = tonumber(ARGV[4])

local bucket = redis.call('HMGET', KEYS[1], 'tokens', 'updated_ms')
local tokens = tonumber(bucket[1])
local updated_ms = tonumber(bucket[2])

-- A bucket that does not exist starts full: a key's first request is never throttled.
if tokens == nil or updated_ms == nil then
  tokens = capacity
  updated_ms = now_ms
end

-- Refill for elapsed time. A clock that went backwards contributes nothing rather than
-- draining the bucket.
local elapsed_ms = math.max(0, now_ms - updated_ms)
tokens = math.min(capacity, tokens + (elapsed_ms / 1000.0) * refill_per_sec)

local allowed = 0
local retry_after_ms = 0
if tokens >= cost then
  allowed = 1
  tokens = tokens - cost
else
  local missing = cost - tokens
  retry_after_ms = math.ceil((missing / refill_per_sec) * 1000)
end

redis.call('HSET', KEYS[1], 'tokens', tokens, 'updated_ms', now_ms)
-- Expire an idle bucket: it would refill to full anyway, so keeping it is pure memory.
-- Twice the refill window covers a bucket that is empty when the last request lands.
redis.call('PEXPIRE', KEYS[1], math.ceil((capacity / refill_per_sec) * 2000))

return {allowed, math.floor(tokens), retry_after_ms}
