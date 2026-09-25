-- Migration: 007_retire_firewall_backends
-- Version: 7
-- Description: Remove empty obsolete CLI firewall tracking and routed policies after the typed root authority cutover

-- Legacy tracked rules are not kernel ownership evidence and cannot be migrated or silently discarded.
CREATE TEMP TABLE retired_firewall_admission_guard (
    legacy_authority_count INTEGER NOT NULL CHECK(legacy_authority_count = 0)
);
INSERT INTO retired_firewall_admission_guard
SELECT (SELECT COUNT(*) FROM iptables_rules)
    + (SELECT COUNT(*) FROM nftables_rules)
    + (SELECT COUNT(*) FROM service_access_policies);
DROP TABLE retired_firewall_admission_guard;

BEGIN IMMEDIATE;

DROP TABLE iptables_rules;
DROP TABLE nftables_rules;
DROP TABLE service_access_policies;

DELETE FROM user_settings
WHERE (category = 'settings' AND key = 'firewall_backend')
   OR (category = 'settings.firewall' AND key = 'iptables_xtcomment');

PRAGMA user_version = 7;
COMMIT;
