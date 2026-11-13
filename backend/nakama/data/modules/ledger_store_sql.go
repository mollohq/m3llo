package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The SQL ledger store (plans/DURABLE-LEDGER.md section 4). It lives in the
// Nakama Postgres database and uses only database/sql.

// ledgerSchemaLockKey is the pg_advisory_lock key of the schema setup. It is
// a fixed number so that two nodes that start together do not both run DDL.
const ledgerSchemaLockKey = 7716001

// ledgerSchema is every statement of the schema. Each one is safe to repeat.
var ledgerSchema = []string{
	`CREATE TABLE IF NOT EXISTS mello_crew_event (
		crew_id    TEXT   NOT NULL,
		ts         BIGINT NOT NULL,
		id         TEXT   NOT NULL,
		type       TEXT   NOT NULL,
		actor_id   TEXT   NOT NULL DEFAULT '',
		score      INT    NOT NULL DEFAULT 0,
		ref        TEXT   NOT NULL DEFAULT '',
		data       JSONB,
		created_ms BIGINT NOT NULL,
		PRIMARY KEY (crew_id, ts, id)
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS mello_crew_event_crew_id_key ON mello_crew_event (crew_id, id)`,
	`CREATE INDEX IF NOT EXISTS mello_crew_event_type_ts_idx ON mello_crew_event (crew_id, type, ts DESC, id)`,
	`CREATE INDEX IF NOT EXISTS mello_crew_event_ref_idx ON mello_crew_event (crew_id, type, ref) WHERE ref <> ''`,
	`CREATE INDEX IF NOT EXISTS mello_crew_event_type_idx ON mello_crew_event (type, ts)`,
	`CREATE TABLE IF NOT EXISTS mello_crew_event_user (
		user_id  TEXT NOT NULL,
		crew_id  TEXT NOT NULL,
		event_id TEXT NOT NULL,
		PRIMARY KEY (user_id, crew_id, event_id)
	)`,
	`CREATE INDEX IF NOT EXISTS mello_crew_event_user_event_idx ON mello_crew_event_user (crew_id, event_id)`,
	`CREATE TABLE IF NOT EXISTS mello_crew_event_week (
		crew_id    TEXT   NOT NULL,
		week_start BIGINT NOT NULL,
		type       TEXT   NOT NULL,
		n          INT    NOT NULL,
		PRIMARY KEY (crew_id, week_start, type)
	)`,
	// The time of the last append of a crew. The 7-day view is cut against
	// it, as the legacy document was trimmed on every append.
	`CREATE TABLE IF NOT EXISTS mello_crew_event_anchor (
		crew_id        TEXT   NOT NULL PRIMARY KEY,
		last_append_ms BIGINT NOT NULL
	)`,
}

// ensureLedgerSchema makes the ledger tables. It holds an advisory lock on
// one connection for the whole run.
func ensureLedgerSchema(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("ledger schema: connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, ledgerSchemaLockKey); err != nil {
		return fmt.Errorf("ledger schema: lock: %w", err)
	}
	// The unlock must run also when ctx is done, or the lock stays with the
	// pooled connection.
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, ledgerSchemaLockKey)

	for _, stmt := range ledgerSchema {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ledger schema: %w", err)
		}
	}
	return nil
}

type sqlLedgerStore struct {
	db *sql.DB
}

func newSQLLedgerStore(db *sql.DB) *sqlLedgerStore {
	return &sqlLedgerStore{db: db}
}

const ledgerEventColumns = `crew_id, id, type, actor_id, ts, score, data`

// scanLedgerEvents reads rows of ledgerEventColumns.
func scanLedgerEvents(rows *sql.Rows) ([]CrewEvent, error) {
	defer rows.Close()
	out := []CrewEvent{}
	for rows.Next() {
		var ev CrewEvent
		var data []byte
		if err := rows.Scan(&ev.CrewID, &ev.ID, &ev.Type, &ev.ActorID, &ev.Timestamp, &ev.Score, &data); err != nil {
			return nil, err
		}
		ev.Data = ledgerDecodeData(data)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ledgerTypeFilter returns " AND type IN ($n, ...)" for the types, with the
// arguments appended to args. It returns "" for an empty list. The store uses
// no array parameters, so the list is one placeholder per type.
func ledgerTypeFilter(types []string, args []interface{}) (string, []interface{}) {
	if len(types) == 0 {
		return "", args
	}
	marks := make([]string, 0, len(types))
	for _, t := range types {
		args = append(args, t)
		marks = append(marks, fmt.Sprintf("$%d", len(args)))
	}
	return " AND type IN (" + strings.Join(marks, ", ") + ")", args
}

func (s *sqlLedgerStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ledgerInsertEvent writes the event row, the user rows and the rollup. It reports
// false, and writes nothing, when the crew already holds the event id.
func ledgerInsertEvent(ctx context.Context, tx *sql.Tx, ev CrewEvent, ref string, createdMs int64) (bool, error) {
	data, err := ledgerEncodeData(ev.Data)
	if err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO mello_crew_event (crew_id, ts, id, type, actor_id, score, ref, data, created_ms)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9)
		 ON CONFLICT DO NOTHING`,
		ev.CrewID, ev.Timestamp, ev.ID, ev.Type, ev.ActorID, ev.Score, ref, string(data), createdMs)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	for _, userID := range eventUserIDs(ev) {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO mello_crew_event_user (user_id, crew_id, event_id) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`,
			userID, ev.CrewID, ev.ID); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO mello_crew_event_week (crew_id, week_start, type, n) VALUES ($1, $2, $3, 1)
		 ON CONFLICT (crew_id, week_start, type) DO UPDATE SET n = mello_crew_event_week.n + 1`,
		ev.CrewID, weekStartOf(ev.Timestamp), ev.Type); err != nil {
		return false, err
	}
	return true, nil
}

func ledgerMoveAnchor(ctx context.Context, tx *sql.Tx, crewID string, anchorMs int64) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO mello_crew_event_anchor (crew_id, last_append_ms) VALUES ($1, $2)
		 ON CONFLICT (crew_id) DO UPDATE
		 SET last_append_ms = GREATEST(mello_crew_event_anchor.last_append_ms, EXCLUDED.last_append_ms)`,
		crewID, anchorMs)
	return err
}

func (s *sqlLedgerStore) Append(ctx context.Context, ev CrewEvent, ref string) (bool, error) {
	if ev.CrewID == "" || ev.ID == "" {
		return false, errors.New("ledger append needs a crew id and an event id")
	}
	nowMs := ledgerNow().UnixMilli()
	inserted := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if inserted, err = ledgerInsertEvent(ctx, tx, ev, ref, nowMs); err != nil {
			return err
		}
		return ledgerMoveAnchor(ctx, tx, ev.CrewID, nowMs)
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}

func (s *sqlLedgerStore) Backfill(ctx context.Context, crewID string, events []CrewEvent, anchorMs int64) (int, error) {
	inserted := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		inserted = 0
		// The backfilled events are older than every event the store already
		// holds, so they sort before them and keep their own order.
		var minCreated sql.NullInt64
		if err := tx.QueryRowContext(ctx,
			`SELECT MIN(created_ms) FROM mello_crew_event WHERE crew_id = $1`, crewID).Scan(&minCreated); err != nil {
			return err
		}
		base := anchorMs
		if minCreated.Valid && minCreated.Int64 < base {
			base = minCreated.Int64
		}
		base -= int64(len(events))
		for i, ev := range events {
			if ev.ID == "" {
				continue
			}
			ev.CrewID = crewID
			ok, err := ledgerInsertEvent(ctx, tx, ev, eventRef(ev), base+int64(i))
			if err != nil {
				return err
			}
			if ok {
				inserted++
			}
		}
		return ledgerMoveAnchor(ctx, tx, crewID, anchorMs)
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

func (s *sqlLedgerStore) UpdateData(ctx context.Context, crewID, eventID string, fn func(json.RawMessage) (json.RawMessage, error)) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var data []byte
		err := tx.QueryRowContext(ctx,
			`SELECT data FROM mello_crew_event WHERE crew_id = $1 AND id = $2 FOR UPDATE`,
			crewID, eventID).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return errLedgerEventNotFound
		}
		if err != nil {
			return err
		}
		out, err := fn(json.RawMessage(data))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE mello_crew_event SET data = $3::jsonb WHERE crew_id = $1 AND id = $2`,
			crewID, eventID, string(out))
		return err
	})
}

func (s *sqlLedgerStore) Delete(ctx context.Context, crewID, eventID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var typ string
		var ts int64
		err := tx.QueryRowContext(ctx,
			`DELETE FROM mello_crew_event WHERE crew_id = $1 AND id = $2 RETURNING type, ts`,
			crewID, eventID).Scan(&typ, &ts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM mello_crew_event_user WHERE crew_id = $1 AND event_id = $2`, crewID, eventID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE mello_crew_event_week SET n = n - 1 WHERE crew_id = $1 AND week_start = $2 AND type = $3`,
			crewID, weekStartOf(ts), typ); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`DELETE FROM mello_crew_event_week WHERE crew_id = $1 AND week_start = $2 AND type = $3 AND n <= 0`,
			crewID, weekStartOf(ts), typ)
		return err
	})
}

func (s *sqlLedgerStore) Between(ctx context.Context, crewID string, fromMs, toMs int64, types []string) ([]CrewEvent, error) {
	filter, args := ledgerTypeFilter(types, []interface{}{crewID, fromMs, toMs})
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+ledgerEventColumns+` FROM mello_crew_event
		 WHERE crew_id = $1 AND ts >= $2 AND ts <= $3`+filter+`
		 ORDER BY ts ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	return scanLedgerEvents(rows)
}

func (s *sqlLedgerStore) NewestN(ctx context.Context, crewID string, types []string, beforeMs int64, n int) ([]CrewEvent, error) {
	if n < 0 {
		n = 0
	}
	filter, args := ledgerTypeFilter(types, []interface{}{crewID, beforeMs, n})
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+ledgerEventColumns+` FROM mello_crew_event
		 WHERE crew_id = $1 AND ts < $2`+filter+`
		 ORDER BY ts DESC, id DESC LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	return scanLedgerEvents(rows)
}

func (s *sqlLedgerStore) CountOlder(ctx context.Context, crewID string, types []string, beforeMs int64) (int, error) {
	// Whole weeks come from the rollup. Only the week that holds beforeMs
	// is counted from event rows.
	openWeek := weekStartOf(beforeMs)
	filter, args := ledgerTypeFilter(types, []interface{}{crewID, openWeek, beforeMs})
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT
		   (SELECT COALESCE(SUM(n), 0) FROM mello_crew_event_week
		     WHERE crew_id = $1 AND week_start < $2`+filter+`)
		 + (SELECT COUNT(*) FROM mello_crew_event
		     WHERE crew_id = $1 AND ts >= $2 AND ts < $3`+filter+`)`, args...).Scan(&n)
	return n, err
}

func (s *sqlLedgerStore) WeeksBefore(ctx context.Context, crewID string, types []string, beforeWeek int64, limit int) ([]weekCount, error) {
	if limit < 0 {
		limit = 0
	}
	filter, args := ledgerTypeFilter(types, []interface{}{crewID, beforeWeek, limit})
	rows, err := s.db.QueryContext(ctx,
		`SELECT week_start, SUM(n) FROM mello_crew_event_week
		 WHERE crew_id = $1 AND week_start < $2`+filter+`
		 GROUP BY week_start HAVING SUM(n) > 0
		 ORDER BY week_start DESC LIMIT $3`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []weekCount
	for rows.Next() {
		var w weekCount
		if err := rows.Scan(&w.WeekStart, &w.N); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *sqlLedgerStore) RecentView(ctx context.Context, crewID string) ([]CrewEvent, error) {
	filter, args := ledgerTypeFilter(ledgerPulseTypes, []interface{}{crewID, ledgerRecentWindowMs})
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+ledgerEventColumns+` FROM mello_crew_event
		 WHERE crew_id = $1
		   AND ts >= (SELECT last_append_ms FROM mello_crew_event_anchor WHERE crew_id = $1) - $2`+filter+`
		 ORDER BY created_ms ASC, id ASC`, args...)
	if err != nil {
		return nil, err
	}
	return scanLedgerEvents(rows)
}

func (s *sqlLedgerStore) ByType(ctx context.Context, typ string, sinceMs int64) ([]CrewEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+ledgerEventColumns+` FROM mello_crew_event
		 WHERE type = $1 AND ts >= $2
		 ORDER BY crew_id ASC, ts ASC, id ASC`, typ, sinceMs)
	if err != nil {
		return nil, err
	}
	return scanLedgerEvents(rows)
}

func (s *sqlLedgerStore) DeleteCrew(ctx context.Context, crewID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"mello_crew_event", "mello_crew_event_user", "mello_crew_event_week", "mello_crew_event_anchor"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE crew_id = $1`, crewID); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *sqlLedgerStore) EventsNamingUser(ctx context.Context, userID string) ([]CrewEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT e.crew_id, e.id, e.type, e.actor_id, e.ts, e.score, e.data
		 FROM mello_crew_event_user u
		 JOIN mello_crew_event e ON e.crew_id = u.crew_id AND e.id = u.event_id
		 WHERE u.user_id = $1
		 ORDER BY e.crew_id ASC, e.ts ASC, e.id ASC`, userID)
	if err != nil {
		return nil, err
	}
	return scanLedgerEvents(rows)
}

// DevLedgerSelftestRPC runs the store conformance on the real ledger tables.
// It writes only to two crews with a new random id and deletes them at the
// end. Gated behind MELLO_ENABLE_DEV_FAULT=1, as the other dev RPCs that
// write. Never enable in production.
func DevLedgerSelftestRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if os.Getenv("MELLO_ENABLE_DEV_FAULT") != "1" {
		return "", runtime.NewError("dev_ledger_selftest disabled (set MELLO_ENABLE_DEV_FAULT=1)", 9)
	}
	if db == nil {
		return "", runtime.NewError("no database", 13)
	}
	if err := ensureLedgerSchema(ctx, db); err != nil {
		return "", runtime.NewError(err.Error(), 13)
	}
	if err := ledgerStoreConformance(ctx, newSQLLedgerStore(db)); err != nil {
		logger.Error("dev_ledger_selftest failed: %v", err)
		return "", runtime.NewError("ledger selftest failed: "+err.Error(), 13)
	}
	logger.Warn("dev_ledger_selftest passed")
	resp, _ := json.Marshal(map[string]interface{}{"success": true, "store": "sql"})
	return string(resp), nil
}
