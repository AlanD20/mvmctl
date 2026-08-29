-- Migration: 002_jailer_launch
-- Version: 2
-- Description: Persist the exact Jailer paired with each VM and snapshot.

ALTER TABLE vm_instances ADD COLUMN jailer_binary_id TEXT NOT NULL DEFAULT '';

UPDATE vm_instances
SET jailer_binary_id = COALESCE((
    SELECT jailer.id
    FROM binaries AS firecracker
    JOIN binaries AS jailer
      ON jailer.type = 'jailer'
     AND jailer.version = firecracker.version
     AND jailer.deleted_at IS NULL
    WHERE firecracker.id = vm_instances.binary_id
      AND firecracker.type = 'firecracker'
    LIMIT 1
), '');

CREATE INDEX idx_vm_instances_jailer_binary ON vm_instances(jailer_binary_id);

PRAGMA user_version = 2;
