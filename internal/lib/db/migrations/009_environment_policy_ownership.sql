-- Ownership receipts supplement policy identity; existing grants remain unclaimed.
ALTER TABLE network_traffic_policies ADD COLUMN claim_id TEXT NOT NULL DEFAULT '';
ALTER TABLE network_traffic_policies ADD COLUMN incarnation TEXT NOT NULL DEFAULT '';
ALTER TABLE vm_traffic_policies ADD COLUMN claim_id TEXT NOT NULL DEFAULT '';
ALTER TABLE vm_traffic_policies ADD COLUMN incarnation TEXT NOT NULL DEFAULT '';
ALTER TABLE exec_policies ADD COLUMN claim_id TEXT NOT NULL DEFAULT '';
ALTER TABLE exec_policies ADD COLUMN incarnation TEXT NOT NULL DEFAULT '';
PRAGMA user_version = 9;
