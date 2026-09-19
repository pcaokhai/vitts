-- Voice catalogue. Public presets have tenant_id null; tenant-owned voices are visible
-- only to their owner (docs/07-permissions.md, US-13 acceptance criterion 2).

-- name: ListPublicVoices :many
select * from voices
where tenant_id is null and enabled
order by id;

-- name: ListVoicesForTenant :many
select * from voices
where enabled and (tenant_id is null or tenant_id = $1)
order by id;

-- name: UpsertVoice :exec
insert into voices (id, display_name, gender, tags, model_version, tenant_id, enabled)
values ($1, $2, $3, $4, $5, $6, $7)
on conflict (id) do update set
    display_name = excluded.display_name,
    model_version = excluded.model_version,
    enabled = excluded.enabled;
