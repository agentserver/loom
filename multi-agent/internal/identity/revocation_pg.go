package identity

import (
	"context"
	"database/sql"
	"log"
	"sync/atomic"
	"time"
)

const (
	pgRevocationPollInterval = 250 * time.Millisecond
	pgRevocationCleanupTTL   = time.Hour
	pgRevocationCleanupEvery = time.Minute
	pgRevocationMaxKeyLen    = 256
)

// pgRevocationChannel is a RevocationChannel backed by a Postgres polling
// loop. It uses the commander_identity_revocations table (see
// internal/commanderhub/authstore/schema_postgres.sql).
//
// Publish inserts a row; Subscribe polls for new rows since the last-seen seq.
// This avoids the long-lived dedicated connection required by LISTEN/NOTIFY and
// survives connection bouncers.
type pgRevocationChannel struct {
	db *sql.DB

	// dropsOversized counts rows dropped because key > pgRevocationMaxKeyLen.
	dropsOversized atomic.Int64
}

// NewPGRevocationChannel creates a pgRevocationChannel using db.
func NewPGRevocationChannel(db *sql.DB) RevocationChannel {
	return &pgRevocationChannel{db: db}
}

// Publish inserts a revocation row for key. key must not be empty and must
// not exceed pgRevocationMaxKeyLen characters. Callers already hold these
// invariants (tokenKey always returns a 64-char hex string) but we guard
// defensively.
func (c *pgRevocationChannel) Publish(ctx context.Context, key string) error {
	if key == "" || len(key) > pgRevocationMaxKeyLen {
		return nil // silently skip; caller logs prefix
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO commander_identity_revocations (key) VALUES ($1)`,
		key,
	)
	return err
}

// Subscribe starts a polling goroutine that calls onRevoke for each new
// revocation row. Returns a stop func that terminates the goroutine.
//
// The goroutine validates each row: empty or oversized keys are logged +
// counted and skipped.
func (c *pgRevocationChannel) Subscribe(ctx context.Context, onRevoke func(string)) (stop func(), err error) {
	// Seed lastSeq from the current maximum so we don't replay historical rows.
	var lastSeq int64
	row := c.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM commander_identity_revocations`,
	)
	if err := row.Scan(&lastSeq); err != nil {
		return func() {}, err
	}

	stopCh := make(chan struct{})
	go c.pollLoop(ctx, onRevoke, &lastSeq, stopCh)
	go c.cleanupLoop(ctx, stopCh)

	return func() { close(stopCh) }, nil
}

func (c *pgRevocationChannel) pollLoop(
	ctx context.Context,
	onRevoke func(string),
	lastSeq *int64,
	stopCh <-chan struct{},
) {
	ticker := time.NewTicker(pgRevocationPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.poll(ctx, onRevoke, lastSeq); err != nil {
				log.Printf("identity revocation: poll error: %v", err)
			}
		}
	}
}

func (c *pgRevocationChannel) poll(
	ctx context.Context,
	onRevoke func(string),
	lastSeq *int64,
) error {
	rows, err := c.db.QueryContext(ctx,
		`SELECT seq, key FROM commander_identity_revocations WHERE seq > $1 ORDER BY seq`,
		*lastSeq,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var seq int64
		var key string
		if err := rows.Scan(&seq, &key); err != nil {
			return err
		}
		if seq > *lastSeq {
			*lastSeq = seq
		}
		if key == "" || len(key) > pgRevocationMaxKeyLen {
			c.dropsOversized.Add(1)
			log.Printf("identity revocation: dropped invalid key len=%d key_prefix=%s",
				len(key), keyPrefix(key))
			continue
		}
		onRevoke(key)
	}
	return rows.Err()
}

func (c *pgRevocationChannel) cleanupLoop(ctx context.Context, stopCh <-chan struct{}) {
	ticker := time.NewTicker(pgRevocationCleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, err := c.db.ExecContext(ctx,
				`DELETE FROM commander_identity_revocations
				  WHERE revoked_at < now() - $1::interval`,
				pgRevocationCleanupTTL.String(),
			)
			if err != nil {
				log.Printf("identity revocation: cleanup error: %v", err)
			}
		}
	}
}
