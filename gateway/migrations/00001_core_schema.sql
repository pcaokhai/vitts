-- Core schema: plans, tenants, api_keys, voices, audio_cache, synth_requests, audit_log.
-- Jobs, job_segments and usage_daily arrive in 0002 (task 2.1).
-- Shape is docs/05-data-model.md; deviations are commented where they exist.
-- +goose Up

create table plans (
    id                      text primary key,
    chars_per_month         bigint not null check (chars_per_month >= 0),
    req_per_minute          int    not null check (req_per_minute > 0),
    max_concurrent_streams  int    not null check (max_concurrent_streams > 0),
    max_job_chars           int    not null default 100000 check (max_job_chars > 0),
    price_usd_cents         int    not null default 0 check (price_usd_cents >= 0),
    overage_usd_per_million numeric(8, 2)
);

create table tenants (
    id         uuid primary key,
    plan_id    text not null references plans (id),
    name       text not null check (length(name) between 1 and 200),
    status     text not null check (status in ('active', 'suspended', 'deleted')),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table api_keys (
    id           uuid primary key,
    tenant_id    uuid not null references tenants (id),
    name         text not null check (length(name) between 1 and 200),
    -- sha256 of a 256-bit secret. The secret itself is shown once and never stored.
    key_hash     bytea not null unique check (octet_length(key_hash) = 32),
    prefix       text not null,
    scopes       text[] not null,
    store_text   boolean not null default false,
    created_at   timestamptz not null default now(),
    last_used_at timestamptz,
    revoked_at   timestamptz
);
-- Auth looks up live keys by tenant; revoked ones are never a lookup target.
create index api_keys_tenant_live_idx on api_keys (tenant_id) where revoked_at is null;

create table voices (
    id            text primary key,
    display_name  text not null,
    gender        text,
    tags          text[] not null default '{}',
    model_version text not null,
    -- null = public preset. Tenant-owned voices are F-15, not yet reachable.
    tenant_id     uuid references tenants (id),
    enabled       boolean not null default true
);
create index voices_tenant_idx on voices (tenant_id) where tenant_id is not null;

create table audio_cache (
    cache_key   bytea primary key check (octet_length(cache_key) = 32),
    s3_key      text not null,
    format      text not null,
    duration_ms int not null check (duration_ms >= 0),
    bytes       bigint not null check (bytes >= 0),
    hit_count   bigint not null default 0,
    created_at  timestamptz not null default now(),
    last_hit_at timestamptz not null default now()
);
-- Eviction scans by last_hit_at (FL-07, task 2.8).
create index audio_cache_last_hit_idx on audio_cache (last_hit_at);

-- Partitioned by month: retention is a detach-and-drop, not a delete of live rows.
create table synth_requests (
    id           uuid not null,
    tenant_id    uuid not null,
    api_key_id   uuid not null,
    voice_id     text not null,
    cache_key    bytea,
    mode         text not null check (mode in ('sync', 'stream', 'job_segment')),
    chars        int not null check (chars >= 0),
    duration_ms  int,
    ttfa_ms      int,
    cached       boolean not null default false,
    status       text not null check (status in ('ok', 'client_cancelled', 'error', 'rejected')),
    error_code   text,
    -- Encrypted, and only when the key has store_text = true (ADR-008).
    request_text bytea,
    created_at   timestamptz not null default now(),
    primary key (id, created_at)
) partition by range (created_at);
create index synth_requests_tenant_created_idx on synth_requests (tenant_id, created_at desc);

-- +goose StatementBegin
-- Creates the partition covering `at` if it does not exist. Called below to seed the
-- current and next month, and by the maintenance job in task 2.8 to stay ahead.
create or replace function ensure_synth_requests_partition(at timestamptz)
returns text
language plpgsql
as $$
declare
    start_at date := date_trunc('month', at)::date;
    end_at   date := (date_trunc('month', at) + interval '1 month')::date;
    name     text := format('synth_requests_%s', to_char(start_at, 'YYYYMM'));
begin
    execute format(
        'create table if not exists %I partition of synth_requests for values from (%L) to (%L)',
        name, start_at, end_at
    );
    return name;
end;
$$;
-- +goose StatementEnd

select ensure_synth_requests_partition(now());
select ensure_synth_requests_partition(now() + interval '1 month');

create table audit_log (
    id         uuid primary key,
    actor      text not null,
    action     text not null,
    target     text,
    detail     jsonb,
    created_at timestamptz not null default now()
);
create index audit_log_created_idx on audit_log (created_at desc);

-- +goose Down
drop table if exists audit_log;
drop table if exists synth_requests;
drop function if exists ensure_synth_requests_partition(timestamptz);
drop table if exists audio_cache;
drop table if exists voices;
drop table if exists api_keys;
drop table if exists tenants;
drop table if exists plans;
