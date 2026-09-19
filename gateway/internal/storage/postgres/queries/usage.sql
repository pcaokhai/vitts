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
