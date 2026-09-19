-- Jobs and their segments. Every tenant-owned read filters on tenant_id: another
-- tenant's job is a 404, not a 403 (docs/07-permissions.md).

-- name: CreateJob :one
insert into jobs (
    id, tenant_id, api_key_id, idempotency_key, status, voice_id, format, sample_rate,
    normalize, params, total_chars, webhook_url, metadata
) values ($1, $2, $3, $4, 'queued', $5, $6, $7, $8, $9, $10, $11, $12)
returning *;

-- name: GetJobByIdempotencyKey :one
select * from jobs where tenant_id = $1 and idempotency_key = $2;

-- name: GetJob :one
select * from jobs where id = $1 and tenant_id = $2;

-- name: ListJobs :many
select * from jobs
where tenant_id = $1 and (sqlc.narg(before)::timestamptz is null or created_at < sqlc.narg(before))
order by created_at desc
limit $2;

-- name: CancelJob :execrows
update jobs set status = 'cancelled', completed_at = now()
where id = $1 and tenant_id = $2
  and status in ('queued', 'segmenting', 'synthesizing', 'merging');

-- name: TransitionJob :execrows
update jobs set status = $3
where id = $1 and status = $2;

-- name: SetJobSegmentsTotal :execrows
update jobs set segments_total = $2, status = 'synthesizing'
where id = $1 and status = 'segmenting';

-- name: FailJob :execrows
update jobs set status = 'failed', error = $2, completed_at = now()
where id = $1 and status not in ('completed', 'failed', 'cancelled');

-- name: CompleteJob :execrows
update jobs set status = 'completed', output_s3_key = $2, duration_ms = $3, completed_at = now()
where id = $1 and status = 'merging';

-- name: CreateJobSegment :exec
insert into job_segments (id, job_id, seq, text_hash, chars, status)
values ($1, $2, $3, $4, $5, 'pending')
on conflict (job_id, seq) do nothing;

-- name: ListJobSegments :many
select * from job_segments where job_id = $1 order by seq;

-- name: ClaimJobSegment :execrows
update job_segments set status = 'running', attempts = attempts + 1
where job_id = $1 and seq = $2 and status in ('pending', 'failed');

-- name: CompleteJobSegment :execrows
with done as (
    update job_segments set status = 'done', s3_key = $3
    where job_id = $1 and seq = $2 and status = 'running'
    returning job_id
)
update jobs set segments_done = segments_done + 1
where id = (select job_id from done);

-- name: FailJobSegment :execrows
update job_segments set status = 'failed', last_error = $3
where job_id = $1 and seq = $2;

-- name: CountUnfinishedSegments :one
select count(*)::bigint from job_segments where job_id = $1 and status <> 'done';

-- name: StuckJobs :many
select * from jobs
where status in ('queued', 'segmenting', 'synthesizing', 'merging')
  and updated_at < $1
order by updated_at
limit $2;
