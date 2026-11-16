package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"time"

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
		pos        BIGINT NOT NULL,
		PRIMARY KEY (crew_id, ts, id)
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS mello_crew_event_crew_id_key ON mello_crew_event (crew_id, id)`,
	`CREATE INDEX IF NOT EXISTS mello_crew_event_type_ts_idx ON mello_crew_event (crew_id, type, ts DESC, id)`,
	`CREATE INDEX IF NOT EXISTS mello_crew_event_ref_idx ON mello_crew_event (crew_id, type, ref) WHERE ref <> ''`,
	`CREATE INDEX IF NOT EXISTS mello_crew_event_type_idx ON mello_crew_event (type, ts)`,
	// pos is the append order of a crew. The 7-day view sorts by it.
	`CREATE INDEX IF NOT EXISTS mello_crew_event_pos_idx ON mello_crew_event (crew_id, pos)`,
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
	// The time of the last append of a crew, and the last position given out.
	// The 7-day view is cut against the time, as the legacy document was
	// trimmed on every append. Every write that gives out positions updates
	// this row first, so the writes of one crew run one after the other.
	`CREATE TABLE IF NOT EXISTS mello_crew_event_anchor (
		crew_id        TEXT   NOT NULL PRIMARY KEY,
		last_append_ms BIGINT NOT NULL,
		last_pos       BIGINT NOT NULL DEFAULT 0
	)`,
	// Ids that Delete removed. A backfill does not store them again.
	`CREATE TABLE IF NOT EXISTS mello_crew_event_deleted (
		crew_id    TEXT   NOT NULL,
		event_id   TEXT   NOT NULL,
		deleted_ms BIGINT NOT NULL,
		PRIMARY KEY (crew_id, event_id)
	)`,
}

// ledgerSchemaLockWait is how long the schema setup waits for the advisory
// lock. Another node holds it only while it runs the same statements.
const ledgerSchemaLockWait = 30 * time.Second

// ensureLedgerSchema makes the ledger tables. It holds an advisory lock on
// one connection for the whole run, and gives up when it does not get the
// lock within ledgerSchemaLockWait.
func ensureLedgerSchema(ctx context.Context, db *sql.DB) error {
	ctx, cancel := context.WithTimeout(ctx, 2*ledgerSchemaLockWait)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("ledger schema: connection: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(ledgerSchemaLockWait)
	for {
		var locked bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, ledgerSchemaLockKey).Scan(&locked); err != nil {
			return fmt.Errorf("ledger schema: lock: %w", err)
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("ledger schema: no advisory lock after %s", ledgerSchemaLockWait)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("ledger schema: lock: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
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

// ledgerSchemaRetry is the wait before the first retry of a failed schema
// setup. It doubles up to ledgerSchemaRetryMax.
const (
	ledgerSchemaRetry    = 5 * time.Second
	ledgerSchemaRetryMax = 5 * time.Minute
)

// startLedgerStore makes the schema and sets the durable store. When the
// schema setup fails it returns at once, so the module starts on the legacy
// document alone, and retries in the background until the setup succeeds.
// The reconcile then copies what the store missed.
func startLedgerStore(ctx context.Context, logger runtime.Logger, wait time.Duration, setup func(context.Context) (ledgerStore, error)) {
	store, err := setup(ctx)
	if err == nil {
		setLedger(store)
		logger.Info("Crew ledger tables ready")
		return
	}
	logger.Error("ledger_schema_failed: %v. Crew events go to the legacy document only. Retry in %s", err, wait)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			store, err := setup(ctx)
			if err == nil {
				setLedger(store)
				logger.Info("Crew ledger tables ready after a retry")
				return
			}
			if wait *= 2; wait > ledgerSchemaRetryMax {
				wait = ledgerSchemaRetryMax
			}
			logger.Error("ledger_schema_failed: %v. Retry in %s", err, wait)
		}
	}()
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

// ledgerInsertEvent writes the event row, the user rows and the rollup. It
// reports false, and writes nothing, when the crew already holds the event id.
func ledgerInsertEvent(ctx context.Context, tx *sql.Tx, ev CrewEvent, ref string, createdMs, pos int64) (bool, error) {
	data, err := ledgerEncodeData(ev.Data)
	if err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO mello_crew_event (crew_id, ts, id, type, actor_id, score, ref, data, created_ms, pos)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)
		 ON CONFLICT DO NOTHING`,
		ev.CrewID, ev.Timestamp, ev.ID, ev.Type, ev.ActorID, ev.Score, ref, string(data), createdMs, pos)
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

// ledgerTakePositions moves the anchor time of a crew forward to atMs and
// takes n positions. It returns the last position before the ones it took.
// The row lock it holds until the end of the transaction makes the writes of
// one crew run one after the other.
func ledgerTakePositions(ctx context.Context, tx *sql.Tx, crewID string, atMs int64, n int) (int64, error) {
	var lastPos int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO mello_crew_event_anchor (crew_id, last_append_ms, last_pos) VALUES ($1, $2, $3)
		 ON CONFLICT (crew_id) DO UPDATE
		 SET last_append_ms = GREATEST(mello_crew_event_anchor.last_append_ms, EXCLUDED.last_append_ms),
		     last_pos = mello_crew_event_anchor.last_pos + EXCLUDED.last_pos
		 RETURNING last_pos`,
		crewID, atMs, n).Scan(&lastPos)
	if err != nil {
		return 0, err
	}
	return lastPos - int64(n), nil
}

func (s *sqlLedgerStore) Append(ctx context.Context, ev CrewEvent, ref string) (bool, error) {
	if ev.CrewID == "" || ev.ID == "" {
		return false, errors.New("ledger append needs a crew id and an event id")
	}
	nowMs := ledgerNow().UnixMilli()
	inserted := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		base, err := ledgerTakePositions(ctx, tx, ev.CrewID, nowMs, 1)
		if err != nil {
			return err
		}
		if inserted, err = ledgerInsertEvent(ctx, tx, ev, ref, nowMs, base+1); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			`DELETE FROM mello_crew_event_deleted WHERE crew_id = $1 AND event_id = $2`, ev.CrewID, ev.ID)
		return err
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}

// ledgerViewWhere is the WHERE clause of the 7-day view and its arguments
// after the crew id, which is $1: the events of the legacy ledger path (pulse
// types and "ledger_" types) of the 7 days before the last append.
func ledgerViewWhere() (string, []interface{}) {
	args := []interface{}{ledgerRecentWindowMs}
	marks := make([]string, 0, len(ledgerPulseTypes))
	for _, t := range ledgerPulseTypes {
		args = append(args, t)
		marks = append(marks, fmt.Sprintf("$%d", len(args)+1))
	}
	// The backslash makes the underscore of the prefix a plain character.
	// pos is 0 for a row that Put stored: it is not in the view.
	where := `crew_id = $1
		AND pos > 0
		AND ts >= (SELECT last_append_ms FROM mello_crew_event_anchor WHERE crew_id = $1) - $2
		AND (type LIKE '` + strings.ReplaceAll(ledgerLegacyTypePrefix, "_", `\_`) + `%' OR type IN (` + strings.Join(marks, ", ") + `))`
	return where, args
}

func (s *sqlLedgerStore) Backfill(ctx context.Context, crewID string, docEvents []CrewEvent, anchorMs int64, docOrderWins bool) (int, error) {
	inserted := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		inserted = 0
		base, err := ledgerTakePositions(ctx, tx, crewID, anchorMs, len(docEvents))
		if err != nil {
			return err
		}
		deleted, err := ledgerDeletedIDs(ctx, tx, crewID)
		if err != nil {
			return err
		}

		isNew := map[string]bool{}
		docIDs := make([]string, 0, len(docEvents))
		for i, ev := range docEvents {
			if ev.ID == "" {
				continue
			}
			docIDs = append(docIDs, ev.ID)
			if deleted[ev.ID] {
				continue
			}
			ev.CrewID = crewID
			ok, err := ledgerInsertEvent(ctx, tx, ev, eventRef(ev), anchorMs, base+int64(i)+1)
			if err != nil {
				return err
			}
			if ok {
				inserted++
				isNew[ev.ID] = true
				continue
			}
			// A row that Put stored is not in the view. The document holds
			// the event, so it now gets a position.
			res, err := tx.ExecContext(ctx,
				`UPDATE mello_crew_event SET pos = $3 WHERE crew_id = $1 AND id = $2 AND pos = 0 AND type = $4`,
				crewID, ev.ID, base+int64(i)+1, ev.Type)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n > 0 {
				isNew[ev.ID] = true
			}
		}

		// Put the view in the wanted order. Only a changed order is written.
		where, args := ledgerViewWhere()
		rows, err := tx.QueryContext(ctx,
			`SELECT id FROM mello_crew_event WHERE `+where+` ORDER BY pos ASC, id ASC`,
			append([]interface{}{crewID}, args...)...)
		if err != nil {
			return err
		}
		var current []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			current = append(current, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		wanted := ledgerMergeOrder(current, docIDs, isNew, docOrderWins)
		if ledgerSameOrder(current, wanted) {
			return nil
		}
		base, err = ledgerTakePositions(ctx, tx, crewID, anchorMs, len(wanted))
		if err != nil {
			return err
		}
		for i, id := range wanted {
			if _, err := tx.ExecContext(ctx,
				`UPDATE mello_crew_event SET pos = $3 WHERE crew_id = $1 AND id = $2`,
				crewID, id, base+int64(i)+1); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// ledgerDeletedIDs returns the ids of a crew that Delete removed.
func ledgerDeletedIDs(ctx context.Context, tx *sql.Tx, crewID string) (map[string]bool, error) {
	deleted := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT event_id FROM mello_crew_event_deleted WHERE crew_id = $1`, crewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		deleted[id] = true
	}
	return deleted, rows.Err()
}

func (s *sqlLedgerStore) Put(ctx context.Context, crewID string, rows []ledgerRow) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	nowMs := ledgerNow().UnixMilli()
	inserted := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		inserted = 0
		deleted, err := ledgerDeletedIDs(ctx, tx, crewID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			ev := row.Event
			if ev.ID == "" {
				return errors.New("ledger put needs an event id")
			}
			if deleted[ev.ID] {
				continue
			}
			ev.CrewID = crewID
			// Position 0: the row is not in the 7-day view. The anchor row
			// is not touched.
			ok, err := ledgerInsertEvent(ctx, tx, ev, row.Ref, nowMs, 0)
			if err != nil {
				return err
			}
			if ok {
				inserted++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return inserted, nil
}

// ledgerRefLockKey is the pg_advisory_xact_lock key of one ref of one type of
// one crew. Two keys that collide only make two writes wait for each other.
func ledgerRefLockKey(crewID, typ, ref string) int64 {
	h := fnv.New64a()
	for _, part := range []string{crewID, typ, ref} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return int64(h.Sum64())
}

const ledgerByRefQuery = `SELECT ` + ledgerEventColumns + ` FROM mello_crew_event
	 WHERE crew_id = $1 AND type = $2 AND ref = $3 AND ref <> ''
	 ORDER BY ts ASC, id ASC`

func (s *sqlLedgerStore) PutUnique(ctx context.Context, crewID string, row ledgerRow) ([]CrewEvent, error) {
	ev := row.Event
	if ev.ID == "" || row.Ref == "" {
		return nil, errors.New("ledger put needs an event id and a ref")
	}
	ev.CrewID = crewID
	nowMs := ledgerNow().UnixMilli()
	var held []CrewEvent
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		held = nil
		// The lock is held until the transaction ends, so the check and the
		// insert of two calls for one ref do not interleave.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, ledgerRefLockKey(crewID, ev.Type, row.Ref)); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, ledgerByRefQuery, crewID, ev.Type, row.Ref)
		if err != nil {
			return err
		}
		if held, err = scanLedgerEvents(rows); err != nil {
			return err
		}
		if len(held) > 0 {
			return nil
		}
		ok, err := ledgerInsertEvent(ctx, tx, ev, row.Ref, nowMs, 0)
		if err != nil {
			return err
		}
		if !ok {
			return errLedgerIDTaken
		}
		_, err = tx.ExecContext(ctx,
			`DELETE FROM mello_crew_event_deleted WHERE crew_id = $1 AND event_id = $2`, crewID, ev.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return held, nil
}

func (s *sqlLedgerStore) ByRef(ctx context.Context, crewID, typ, ref string) ([]CrewEvent, error) {
	if ref == "" {
		return []CrewEvent{}, nil
	}
	rows, err := s.db.QueryContext(ctx, ledgerByRefQuery, crewID, typ, ref)
	if err != nil {
		return nil, err
	}
	return scanLedgerEvents(rows)
}

// ledgerByIDsChunk bounds the placeholders of one ByIDs query.
const ledgerByIDsChunk = 200

func (s *sqlLedgerStore) ByIDs(ctx context.Context, crewID string, ids []string) ([]CrewEvent, error) {
	out := []CrewEvent{}
	for start := 0; start < len(ids); start += ledgerByIDsChunk {
		end := start + ledgerByIDsChunk
		if end > len(ids) {
			end = len(ids)
		}
		args := []interface{}{crewID}
		marks := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			args = append(args, id)
			marks = append(marks, fmt.Sprintf("$%d", len(args)))
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT `+ledgerEventColumns+` FROM mello_crew_event
			 WHERE crew_id = $1 AND id IN (`+strings.Join(marks, ", ")+`)`, args...)
		if err != nil {
			return nil, err
		}
		events, err := scanLedgerEvents(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}
	return out, nil
}

func (s *sqlLedgerStore) ByRefs(ctx context.Context, crewID, typ string, refs []string) ([]CrewEvent, error) {
	out := []CrewEvent{}
	wanted := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref != "" {
			wanted = append(wanted, ref)
		}
	}
	for start := 0; start < len(wanted); start += ledgerByIDsChunk {
		end := start + ledgerByIDsChunk
		if end > len(wanted) {
			end = len(wanted)
		}
		args := []interface{}{crewID, typ}
		marks := make([]string, 0, end-start)
		for _, ref := range wanted[start:end] {
			args = append(args, ref)
			marks = append(marks, fmt.Sprintf("$%d", len(args)))
		}
		rows, err := s.db.QueryContext(ctx,
			`SELECT `+ledgerEventColumns+` FROM mello_crew_event
			 WHERE crew_id = $1 AND type = $2 AND ref <> '' AND ref IN (`+strings.Join(marks, ", ")+`)`, args...)
		if err != nil {
			return nil, err
		}
		events, err := scanLedgerEvents(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}
	return out, nil
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

// ledgerRemoveEvent deletes the event row, its user rows and its rollup count.
func ledgerRemoveEvent(ctx context.Context, tx *sql.Tx, crewID, eventID string) error {
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
}

func (s *sqlLedgerStore) Delete(ctx context.Context, crewID, eventID string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := ledgerRemoveEvent(ctx, tx, crewID, eventID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO mello_crew_event_deleted (crew_id, event_id, deleted_ms) VALUES ($1, $2, $3)
			 ON CONFLICT (crew_id, event_id) DO UPDATE SET deleted_ms = EXCLUDED.deleted_ms`,
			crewID, eventID, ledgerNow().UnixMilli())
		return err
	})
}

func (s *sqlLedgerStore) Prune(ctx context.Context, crewID string, eventIDs []string) error {
	if len(eventIDs) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range eventIDs {
			if err := ledgerRemoveEvent(ctx, tx, crewID, id); err != nil {
				return err
			}
		}
		return nil
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
	where, args := ledgerViewWhere()
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+ledgerEventColumns+` FROM mello_crew_event WHERE `+where+` ORDER BY pos ASC, id ASC`,
		append([]interface{}{crewID}, args...)...)
	if err != nil {
		return nil, err
	}
	events, err := scanLedgerEvents(rows)
	if err != nil {
		return nil, err
	}
	for i := range events {
		events[i].Type, _ = ledgerReaderType(events[i].Type)
	}
	return events, nil
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
		for _, table := range []string{"mello_crew_event", "mello_crew_event_user", "mello_crew_event_week", "mello_crew_event_anchor", "mello_crew_event_deleted"} {
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
