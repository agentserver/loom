-- Rollback script for shared-daemon-registry schema.
-- Manual down migration for ops rolling back across the shared-registry PR.
--
-- Usage: psql -U observer -d observer < schema_postgres_rollback.sql
--
-- After rollback, UI URLs that bookmarked short_ids will break until re-roll-forward.

DROP TABLE IF EXISTS commander_telemetry_buckets;
DROP TABLE IF EXISTS commander_forward_nonces;
DROP TABLE IF EXISTS commander_turns;
DROP TABLE IF EXISTS commander_daemons;
