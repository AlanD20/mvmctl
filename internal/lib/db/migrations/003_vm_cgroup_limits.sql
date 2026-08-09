-- Migration: 003_vm_cgroup_limits
-- Version: 3
-- Description: Persist typed cgroup-v2 limits for every VM.

ALTER TABLE vm_instances ADD COLUMN cgroup_limits TEXT NOT NULL DEFAULT
    '{"policy_version":0,"cpu_quota_micros":100000,"cpu_period_micros":100000,"cpu_weight":100,"memory_high_bytes":671088640,"memory_max_bytes":671088640,"swap_max_bytes":0,"pids_max":256}';

PRAGMA user_version = 3;
