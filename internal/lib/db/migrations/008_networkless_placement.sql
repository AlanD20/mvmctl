-- Migration: 008_networkless_placement
-- Version: 8
-- v0.3.0 is a clean installation: old version-7 VM tables are not adopted.
-- The fresh initial schema already contains the placement column and nullable FK.
SELECT network_placement FROM vm_instances LIMIT 0;

PRAGMA user_version = 8;
