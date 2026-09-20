-- Usage reads for quota reconciliation. synth_requests is the system of record; the
-- usage_daily rollup arrives with migration 0002 (task 2.1) and can replace this scan
-- once it exists.

-- name: SumCharsByTenantSince :many
select tenant_id, sum(chars)::bigint as chars
from synth_requests
where created_at >= $1 and created_at < $2
  and status in ('ok', 'client_cancelled')
group by tenant_id;

-- name: InsertSynthRequest :exec
insert into synth_requests (
    id, tenant_id, api_key_id, voice_id, cache_key, mode, chars,
    duration_ms, ttfa_ms, cached, status, error_code, created_at
) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
on conflict (id, created_at) do nothing;

-- name: RollupUsageDaily :exec
-- Upsert the daily rollup from the request log. Re-running it for a window is safe:
-- the aggregate is recomputed, not incremented (FL-07).
insert into usage_daily (tenant_id, day, chars, audio_ms, requests, cache_hits)
select
    tenant_id,
    (created_at at time zone 'UTC')::date as day,
    sum(chars)::bigint,
    coalesce(sum(duration_ms), 0)::bigint,
    count(*)::bigint,
    count(*) filter (where cached)::bigint
from synth_requests
where created_at >= $1 and created_at < $2
  and status in ('ok', 'client_cancelled')
group by tenant_id, (created_at at time zone 'UTC')::date
on conflict (tenant_id, day) do update set
    chars = excluded.chars,
    audio_ms = excluded.audio_ms,
    requests = excluded.requests,
    cache_hits = excluded.cache_hits;

-- name: UsageByDay :many
select day, chars, audio_ms, requests, cache_hits
from usage_daily
where tenant_id = $1 and day >= $2 and day <= $3
order by day;

-- name: EvictableCacheEntries :many
select cache_key, s3_key from audio_cache
where last_hit_at < $1
order by last_hit_at
limit $2;

-- name: DeleteCacheEntry :exec
delete from audio_cache where cache_key = $1;
