-- Development seed. NOT a migration and never run in production.
--
-- The plan tiers below are placeholders so a local stack can create a tenant. Pricing is
-- an open product decision: docs/01-PRD.md quotes a competitor at $16 per million
-- characters but defines no tiers of our own. Replace these with the real figures — and
-- move them into a migration — once pricing is decided.
insert into plans (
    id, chars_per_month, req_per_minute, max_concurrent_streams, max_job_chars,
    price_usd_cents, overage_usd_per_million
) values
    ('dev', 1000000, 120, 5, 100000, 0, null)
on conflict (id) do nothing;
