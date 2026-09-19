-- Jobs, their segments, and the daily usage rollup. Shape is docs/05-data-model.md;
-- deviations are commented where they exist.
-- +goose Up

create table jobs (
    id               uuid primary key,
    tenant_id        uuid not null references tenants (id),
    api_key_id       uuid not null,
    idempotency_key  text not null,
    status           text not null check (status in
                     ('queued', 'segmenting', 'synthesizing', 'merging',
                      'completed', 'failed', 'cancelled')),
    voice_id         text not null,
    format           text not null check (format in ('mp3', 'ogg_opus', 'wav')),
    sample_rate      int not null check (sample_rate in (8000, 16000, 24000, 48000)),
    normalize        boolean not null,
    params           jsonb not null,
    total_chars      int not null check (total_chars >= 0),
    segments_total   int check (segments_total >= 0),
    segments_done    int not null default 0 check (segments_done >= 0),
    output_s3_key    text,
    duration_ms      int,
    webhook_url      text,
    webhook_attempts int not null default 0 check (webhook_attempts >= 0),
    metadata         jsonb not null default '{}',
    error            text,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now(),
    completed_at     timestamptz,
    -- Idempotency is per tenant: two tenants may reuse the same key (FL-03 step 1).
    unique (tenant_id, idempotency_key)
);
create index jobs_tenant_created_idx on jobs (tenant_id, created_at desc);
-- Partial index: the orchestrator and the reconciler only ever scan live jobs.
create index jobs_active_idx on jobs (status)
    where status in ('queued', 'segmenting', 'synthesizing', 'merging');

create table job_segments (
    id         uuid primary key,
    job_id     uuid not null references jobs (id) on delete cascade,
    seq        int not null check (seq >= 0),
    -- Equals the audio cache key when the segment was cacheable (ADR-006).
    text_hash  bytea not null check (octet_length(text_hash) = 32),
    chars      int not null check (chars >= 0),
    status     text not null check (status in ('pending', 'running', 'done', 'failed')),
    attempts   int not null default 0 check (attempts >= 0),
    s3_key     text,
    last_error text,
    unique (job_id, seq)
);
-- The segment consumer claims work by status; the index keeps that scan off the table.
create index job_segments_pending_idx on job_segments (job_id, status) where status <> 'done';

create table usage_daily (
    tenant_id  uuid not null,
    day        date not null,
    chars      bigint not null default 0 check (chars >= 0),
    audio_ms   bigint not null default 0 check (audio_ms >= 0),
    requests   bigint not null default 0 check (requests >= 0),
    cache_hits bigint not null default 0 check (cache_hits >= 0),
    primary key (tenant_id, day)
);

-- +goose StatementBegin
-- Keeps updated_at honest without every writer remembering to set it. Job state
-- transitions are conditional updates (docs/05-data-model.md § 4), and a stale
-- updated_at would make the reconciler's "stuck job" query lie.
create or replace function set_updated_at()
returns trigger
language plpgsql
as $$
begin
    new.updated_at := now();
    return new;
end;
$$;
-- +goose StatementEnd

create trigger jobs_set_updated_at
    before update on jobs
    for each row
    execute function set_updated_at();

-- +goose Down
drop trigger if exists jobs_set_updated_at on jobs;
drop function if exists set_updated_at();
drop table if exists usage_daily;
drop table if exists job_segments;
drop table if exists jobs;
