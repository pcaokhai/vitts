-- Tenant and plan reads. Every tenant-owned query filters on tenant_id; a tenant that is
-- not active must not be served (US-05).

-- name: GetTenant :one
select t.*, sqlc.embed(p)
from tenants t
join plans p on p.id = t.plan_id
where t.id = $1;

-- name: CreateTenant :one
insert into tenants (id, plan_id, name, status)
values ($1, $2, $3, 'active')
returning *;

-- name: ListPlans :many
select * from plans order by id;

-- name: GetPlan :one
select * from plans where id = $1;

-- name: CreateAuditEntry :exec
insert into audit_log (id, actor, action, target, detail)
values ($1, $2, $3, $4, $5);
