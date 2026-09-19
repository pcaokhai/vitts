-- Audio cache catalogue. Redis is the fast index; these rows are what survives a Redis
-- flush and what the eviction job scans (FL-07, task 2.8).

-- name: UpsertCacheEntry :exec
insert into audio_cache (cache_key, s3_key, format, duration_ms, bytes)
values ($1, $2, $3, $4, $5)
on conflict (cache_key) do update set
    s3_key = excluded.s3_key,
    format = excluded.format,
    duration_ms = excluded.duration_ms,
    bytes = excluded.bytes,
    last_hit_at = now();

-- name: TouchCacheEntry :exec
update audio_cache set hit_count = hit_count + 1, last_hit_at = now()
where cache_key = $1;

-- name: GetCacheEntry :one
select * from audio_cache where cache_key = $1;
