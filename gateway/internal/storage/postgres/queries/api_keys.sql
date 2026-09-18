-- API key lookup. The secret is never stored: callers hash it and look up by digest,
-- and the comparison happens on the hash (.claude/rules/security.md).

-- name: GetAPIKeyByHash :one
select k.*, sqlc.embed(t), sqlc.embed(p)
from api_keys k
join tenants t on t.id = k.tenant_id
join plans p on p.id = t.plan_id
where k.key_hash = $1 and k.revoked_at is null;

-- name: CreateAPIKey :one
insert into api_keys (id, tenant_id, name, key_hash, prefix, scopes, store_text)
values ($1, $2, $3, $4, $5, $6, $7)
returning *;

-- name: ListAPIKeys :many
select * from api_keys
where tenant_id = $1 and (revoked_at is null or sqlc.arg(include_revoked)::bool)
order by created_at desc;

-- name: RevokeAPIKey :execrows
update api_keys set revoked_at = now()
where id = $1 and tenant_id = $2 and revoked_at is null;

-- name: TouchAPIKeyLastUsed :exec
update api_keys set last_used_at = now() where id = $1;
