package commanderhub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"sync/atomic"
	"time"
)

// SQL statements as package-level consts so unit tests can assert exact
// shape via sqlmock.QueryMatcherEqual. Indentation/whitespace must match
// what the production code passes to db.ExecContext/QueryRowContext.

const connectUpsertSQL = `INSERT INTO commander_daemons (user_id, workspace_id, short_id, connection_id, display_name, kind, driver_version, capabilities, owning_instance_url, last_seen_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, now(), now()) ON CONFLICT (user_id, workspace_id, short_id) DO UPDATE SET connection_id = EXCLUDED.connection_id, display_name = EXCLUDED.display_name, kind = EXCLUDED.kind, driver_version = EXCLUDED.driver_version, capabilities = EXCLUDED.capabilities, owning_instance_url = EXCLUDED.owning_instance_url, last_seen_at = now()`

const heartbeatUpsertSQL = `INSERT INTO commander_daemons (user_id, workspace_id, short_id, connection_id, display_name, kind, driver_version, capabilities, owning_instance_url, last_seen_at, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, now(), now()) ON CONFLICT (user_id, workspace_id, short_id) DO UPDATE SET last_seen_at = now(), display_name = EXCLUDED.display_name, kind = EXCLUDED.kind, driver_version = EXCLUDED.driver_version, capabilities = EXCLUDED.capabilities WHERE commander_daemons.owning_instance_url = EXCLUDED.owning_instance_url AND commander_daemons.connection_id = EXCLUDED.connection_id`

const removeSQL = `DELETE FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND short_id = $3 AND owning_instance_url = $4 AND connection_id = $5`

const lookupRemoteSQL = `SELECT owning_instance_url, short_id, display_name, kind, driver_version, capabilities, last_seen_at FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND short_id = $3 AND last_seen_at > $4`

const listAllSQL = `SELECT short_id, display_name, kind, driver_version, capabilities, last_seen_at, owning_instance_url FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND last_seen_at > $3 ORDER BY display_name`

const sweepDaemonsSQL = `DELETE FROM commander_daemons WHERE last_seen_at < $1`

const sweepNoncesSQL = `DELETE FROM commander_forward_nonces WHERE received_at < $1`

const sweepTelemetryBucketsSQL = `DELETE FROM commander_telemetry_buckets WHERE updated_at < $1`

const confirmOwnershipSQL = `SELECT owning_instance_url, connection_id FROM commander_daemons WHERE user_id = $1 AND workspace_id = $2 AND short_id = $3`

const (
	defaultOnlineTTL      = 45 * time.Second
	defaultDeleteAfter    = 5 * time.Minute
	defaultHeartbeatEvery = 15 * time.Second
	defaultSweepEvery     = 30 * time.Second
	defaultNonceTTL       = 120 * time.Second
)

type sharedRegistry struct {
	db                              *sql.DB
	advertiseURL                    string
	onlineTTL                       time.Duration
	deleteAfter                     time.Duration
	heartbeatEvery                  time.Duration
	sweepEvery                      time.Duration
	nonceTTL                        time.Duration
	sweepErrCount                   int32
	sweepNoncesErrCount             int32
	sweepTelemetryBucketsErrCount   int32
}

func newSharedRegistry(db *sql.DB, advertiseURL string) *sharedRegistry {
	return &sharedRegistry{
		db:             db,
		advertiseURL:   advertiseURL,
		onlineTTL:      defaultOnlineTTL,
		deleteAfter:    defaultDeleteAfter,
		heartbeatEvery: defaultHeartbeatEvery,
		sweepEvery:     defaultSweepEvery,
		nonceTTL:       defaultNonceTTL,
	}
}

// connectUpsert: claim ownership on new WS connect. INSERT ... ON CONFLICT
// DO UPDATE without ownership guard — the new connect is allowed to take
// ownership. Previous owner's heartbeat will see 0 rows (its WHERE
// includes connection_id) and exit.
func (s *sharedRegistry) connectUpsert(ctx context.Context, dc *daemonConn) error {
	dc.metaMu.Lock()
	capsList := make([]string, 0, len(dc.capabilities))
	for cap, on := range dc.capabilities {
		if on {
			capsList = append(capsList, cap)
		}
	}
	dc.metaMu.Unlock()
	sort.Strings(capsList)
	capsJSON, _ := json.Marshal(capsList)
	_, err := s.db.ExecContext(ctx, connectUpsertSQL,
		dc.owner.userID, dc.owner.workspaceID, dc.shortID, dc.id,
		dc.displayName, dc.kind, dc.driverVersion, string(capsJSON),
		s.advertiseURL)
	return err
}

// heartbeatUpsert: refresh last_seen_at ONLY when this pod + this exact
// connection still owns the row. 0 rows => ownership lost (sibling pod or
// newer same-pod connection took over).
//
// Implemented per spec v19 §"sharedRegistry methods" as an UPSERT with
// ownership-guarded WHERE clause (NOT a plain UPDATE). Two distinct
// behaviors arise from the WHERE:
//   - Row exists AND we still own it -> SET fires -> RowsAffected=1.
//   - Row exists AND sibling owns it -> SET skipped (WHERE false) -> RowsAffected=0.
//   - Row missing (sweep deleted it during a long PG hiccup) -> INSERT
//     path fires -> RowsAffected=1 -> we re-claim ownership. This is
//     intentional self-healing (see spec v19 §"Daemon admission + teardown
//     ordering" and the sweep TTL discussion: deleteAfter=5min >>
//     onlineTTL=45s so this case is rare).
func (s *sharedRegistry) heartbeatUpsert(ctx context.Context, dc *daemonConn) (stillOwn bool, err error) {
	dc.metaMu.Lock()
	capsList := make([]string, 0, len(dc.capabilities))
	for cap, on := range dc.capabilities {
		if on {
			capsList = append(capsList, cap)
		}
	}
	dc.metaMu.Unlock()
	sort.Strings(capsList)
	capsJSON, _ := json.Marshal(capsList)
	res, err := s.db.ExecContext(ctx, heartbeatUpsertSQL,
		dc.owner.userID, dc.owner.workspaceID, dc.shortID, dc.id,
		dc.displayName, dc.kind, dc.driverVersion, string(capsJSON),
		s.advertiseURL)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// remove: ownership + connection-id-guarded DELETE.
func (s *sharedRegistry) remove(ctx context.Context, o owner, shortID, connectionID string) error {
	_, err := s.db.ExecContext(ctx, removeSQL,
		o.userID, o.workspaceID, shortID, s.advertiseURL, connectionID)
	return err
}

// lookupRemote: peerURL+info iff fresh AND peer-owned.
func (s *sharedRegistry) lookupRemote(ctx context.Context, o owner, shortID string) (string, DaemonInfo, bool, error) {
	row := s.db.QueryRowContext(ctx, lookupRemoteSQL,
		o.userID, o.workspaceID, shortID, time.Now().Add(-s.onlineTTL))
	var ownerURL, displayName, kind, driverVersion, capabilitiesJSON string
	var sid string
	var lastSeen time.Time
	if err := row.Scan(&ownerURL, &sid, &displayName, &kind, &driverVersion, &capabilitiesJSON, &lastSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", DaemonInfo{}, false, nil
		}
		return "", DaemonInfo{}, false, err
	}
	if ownerURL == s.advertiseURL {
		return "", DaemonInfo{}, false, nil
	}
	var capabilities []string
	_ = json.Unmarshal([]byte(capabilitiesJSON), &capabilities)
	return ownerURL, DaemonInfo{
		DaemonID:      sid,
		ShortID:       sid,
		DisplayName:   displayName,
		Kind:          kind,
		DriverVersion: driverVersion,
		Capabilities:  capabilities,
		LastSeenAt:    lastSeen.UTC().Format(time.RFC3339Nano),
	}, true, nil
}

// runHeartbeatOnce executes one tick body: heartbeatUpsert + handle
// result. Returns false when the loop must stop (ownership lost OR
// ctx canceled). Returns true otherwise (still own, or transient PG
// error — caller continues looping).
//
// Exposed as a method (not a closure) so tests can call it directly
// without relying on timer races.
func (s *sharedRegistry) runHeartbeatOnce(ctx context.Context, dc *daemonConn) bool {
	hbCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	stillOwn, err := s.heartbeatUpsert(hbCtx, dc)
	switch {
	case err != nil:
		// Transient PG error — rate-limited log; caller continues looping.
		n := dc.heartbeatErrCount.Add(1)
		if n%5 == 1 {
			log.Printf("commanderhub: heartbeatUpsert short_id=%s conn_id=%s pod=%s err=%v",
				dc.shortID, dc.id, s.advertiseURL, err)
		}
		return true
	case !stillOwn:
		log.Printf("commanderhub: heartbeat ownership lost short_id=%s conn_id=%s pod=%s; force-closing WS",
			dc.shortID, dc.id, s.advertiseURL)
		dc.ownershipLost.Store(true)
		// Force-close so the read loop wakes with io.EOF; ServeHTTP
		// defers then run localReg.removeIf + sharedReg.remove,
		// neither of which delete the new owner's state (both are
		// connection_id-guarded).
		_ = dc.conn.Close()
		return false
	default:
		dc.heartbeatErrCount.Store(0)
		return true
	}
}

// runHeartbeat ticks every s.heartbeatEvery, calling runHeartbeatOnce.
// Exits on ctx cancel OR when runHeartbeatOnce returns false (ownership
// loss).
func (s *sharedRegistry) runHeartbeat(ctx context.Context, dc *daemonConn) {
	ticker := time.NewTicker(s.heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if !s.runHeartbeatOnce(ctx, dc) {
			return
		}
	}
}

// listAll: every fresh row for owner (this pod + peers).
func (s *sharedRegistry) listAll(ctx context.Context, o owner) ([]DaemonInfo, error) {
	rows, err := s.db.QueryContext(ctx, listAllSQL,
		o.userID, o.workspaceID, time.Now().Add(-s.onlineTTL))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]DaemonInfo, 0, 8)
	for rows.Next() {
		var sid, displayName, kind, driverVersion, capsJSON, ownerURL string
		var lastSeen time.Time
		if err := rows.Scan(&sid, &displayName, &kind, &driverVersion, &capsJSON, &lastSeen, &ownerURL); err != nil {
			return nil, err
		}
		var caps []string
		_ = json.Unmarshal([]byte(capsJSON), &caps)
		out = append(out, DaemonInfo{
			DaemonID:      sid,
			ShortID:       sid,
			DisplayName:   displayName,
			Kind:          kind,
			DriverVersion: driverVersion,
			Capabilities:  caps,
			LastSeenAt:    lastSeen.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, rows.Err()
}

// sweep: delete stale daemons (last_seen_at < now - deleteAfter).
func (s *sharedRegistry) sweep(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, sweepDaemonsSQL,
		time.Now().Add(-s.deleteAfter))
	return err
}

// sweepNonces: delete stale nonces (received_at < now - nonceTTL).
func (s *sharedRegistry) sweepNonces(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, sweepNoncesSQL,
		time.Now().Add(-s.nonceTTL))
	return err
}

// sweepTelemetryBuckets: delete stale buckets (updated_at < now - 1h).
func (s *sharedRegistry) sweepTelemetryBuckets(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, sweepTelemetryBucketsSQL,
		time.Now().Add(-1*time.Hour))
	return err
}

// runSweepOnce executes one tick body: all three sweeps. Errors are
// logged but not fatal — the loop continues on transient PG issues.
//
// Exposed as a method (not a closure) so tests can call it directly
// without relying on timer races.
func (s *sharedRegistry) runSweepOnce(ctx context.Context) {
	sweepCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := s.sweep(sweepCtx); err != nil {
		n := atomic.AddInt32(&s.sweepErrCount, 1)
		if n%5 == 1 {
			log.Printf("commanderhub: sweep daemons pod=%s err=%v",
				s.advertiseURL, err)
		}
	}

	if err := s.sweepNonces(sweepCtx); err != nil {
		n := atomic.AddInt32(&s.sweepNoncesErrCount, 1)
		if n%5 == 1 {
			log.Printf("commanderhub: sweep nonces pod=%s err=%v",
				s.advertiseURL, err)
		}
	}

	if err := s.sweepTelemetryBuckets(sweepCtx); err != nil {
		n := atomic.AddInt32(&s.sweepTelemetryBucketsErrCount, 1)
		if n%5 == 1 {
			log.Printf("commanderhub: sweep telemetry buckets pod=%s err=%v",
				s.advertiseURL, err)
		}
	}
}

// runSweep ticks every s.sweepEvery, calling runSweepOnce.
// Exits on ctx cancel.
func (s *sharedRegistry) runSweep(ctx context.Context) {
	ticker := time.NewTicker(s.sweepEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.runSweepOnce(ctx)
	}
}
