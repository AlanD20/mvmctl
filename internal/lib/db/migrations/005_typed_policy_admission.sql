-- Migration: 005_typed_policy_admission
-- Version: 5
-- Description: Reject databases created by the pre-foundation version-4 policy schema

-- Version 4 existed before typed policy identifiers became closed canonical
-- digests. A database already reporting user_version=4 skips migration 004,
-- so admission must distinguish that old schema from a fresh run of the
-- rewritten migration and must reject any carried legacy authority.
CREATE TEMP TABLE typed_policy_v5_admission_guard (
    legacy_policy_authority_count INTEGER NOT NULL CHECK(legacy_policy_authority_count = 0)
);
INSERT INTO typed_policy_v5_admission_guard
SELECT
    3 - (
        SELECT COUNT(*)
        FROM sqlite_master
        WHERE type = 'table'
          AND name IN ('network_traffic_policies', 'vm_traffic_policies', 'exec_policies')
          AND instr(
              replace(replace(replace(replace(lower(sql), ' ', ''), char(9), ''), char(10), ''), char(13), ''),
              'idtextprimarykeycheck(length(id)=64andidnotglob''*[^0-9a-f]*'')'
          ) > 0
    )
    + (SELECT COUNT(*) FROM vm_instances WHERE remote_exec <> 0)
    + (SELECT COUNT(*) FROM service_access_policies)
    + (SELECT COUNT(*) FROM iptables_rules)
    + (SELECT COUNT(*) FROM nftables_rules);
DROP TABLE typed_policy_v5_admission_guard;

PRAGMA user_version = 5;
