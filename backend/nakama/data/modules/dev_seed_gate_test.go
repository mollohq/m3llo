package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"sync/atomic"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

// dev_seed_state and dev_seed_chat rewrite the data of the users and crews
// with the seed names, whoever owns them. They exist only on an install that
// sets MELLO_ENABLE_DEV_SEED=1. On every other install a call gets the answer
// of an RPC that does not exist, and nothing is read or written.

// seedGateDB is a database with no rows. It counts the statements, so a test
// sees if the RPC went to the database.
type seedGateDB struct{ statements atomic.Int32 }

func (d *seedGateDB) Connect(context.Context) (driver.Conn, error) { return seedGateConn{d}, nil }
func (d *seedGateDB) Driver() driver.Driver                        { return seedGateDriver{d} }

type seedGateDriver struct{ db *seedGateDB }

func (d seedGateDriver) Open(string) (driver.Conn, error) { return seedGateConn{d.db}, nil }

type seedGateConn struct{ db *seedGateDB }

func (c seedGateConn) Prepare(string) (driver.Stmt, error) { return seedGateStmt{c.db}, nil }
func (c seedGateConn) Close() error                        { return nil }
func (c seedGateConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

type seedGateStmt struct{ db *seedGateDB }

func (s seedGateStmt) Close() error  { return nil }
func (s seedGateStmt) NumInput() int { return -1 }
func (s seedGateStmt) Exec([]driver.Value) (driver.Result, error) {
	s.db.statements.Add(1)
	return driver.RowsAffected(0), nil
}
func (s seedGateStmt) Query([]driver.Value) (driver.Rows, error) {
	s.db.statements.Add(1)
	return seedGateRows{}, nil
}

type seedGateRows struct{}

func (seedGateRows) Columns() []string         { return []string{"id", "display_name"} }
func (seedGateRows) Close() error              { return nil }
func (seedGateRows) Next([]driver.Value) error { return io.EOF }

type seedRPC func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error)

var devSeedRPCs = map[string]seedRPC{
	"dev_seed_state": DevSeedStateRPC,
	"dev_seed_chat":  DevSeedChatRPC,
}

// callSeedRPC runs one seed RPC on an empty database and an empty Nakama
// fake. It returns the error, the number of database statements and the
// number of stored objects.
func callSeedRPC(t *testing.T, rpc seedRPC, ctx context.Context) (error, int, int) {
	t.Helper()
	gate := &seedGateDB{}
	db := sql.OpenDB(gate)
	defer db.Close()
	nk := newFakeGuestNk()
	_, err := rpc(ctx, testLogger(), db, nk, `{"reset":true}`)
	nk.mu.Lock()
	stored := len(nk.storage)
	nk.mu.Unlock()
	return err, int(gate.statements.Load()), stored
}

func TestDevSeedRPCs_WithoutTheVariableAnswerAsAnRPCThatDoesNotExist(t *testing.T) {
	// Only the value 1 turns the seed on. An unset variable is the state of
	// production.
	for _, value := range []string{"", "0", "true", "yes", " 1"} {
		t.Setenv("MELLO_ENABLE_DEV_SEED", value)
		for name, rpc := range devSeedRPCs {
			for caller, ctx := range map[string]context.Context{
				"a session":    ctxWithUser("some-device-account"),
				"the HTTP key": context.Background(),
			} {
				err, statements, stored := callSeedRPC(t, rpc, ctx)
				re, ok := err.(*runtime.Error)
				if !ok || re.Code != 5 || re.Message != "RPC function not found" {
					t.Errorf("%s by %s with MELLO_ENABLE_DEV_SEED=%q: err = %v, want NOT_FOUND (5) \"RPC function not found\"", name, caller, value, err)
				}
				if statements != 0 || stored != 0 {
					t.Errorf("%s by %s with MELLO_ENABLE_DEV_SEED=%q: %d database statements and %d stored objects, want none", name, caller, value, statements, stored)
				}
			}
		}
	}
}

func TestDevSeedRPCs_WithTheVariableRunTheSeed(t *testing.T) {
	t.Setenv("MELLO_ENABLE_DEV_SEED", "1")
	for name, rpc := range devSeedRPCs {
		// The database has no seed user, so the seed stops at its first
		// check: FAILED_PRECONDITION (9), "run seed.sh first". That answer
		// comes from the seed itself, after the gate.
		err, statements, _ := callSeedRPC(t, rpc, ctxWithUser("alice-id"))
		if re, ok := err.(*runtime.Error); !ok || re.Code != 9 {
			t.Errorf("%s with MELLO_ENABLE_DEV_SEED=1: err = %v, want the seed's own FAILED_PRECONDITION (9)", name, err)
		}
		if statements == 0 {
			t.Errorf("%s with MELLO_ENABLE_DEV_SEED=1 did not look for the seed users", name)
		}
	}
}

// seedRegistrar records the RPC ids that the module registers.
type seedRegistrar struct{ ids []string }

func (r *seedRegistrar) RegisterRpc(id string, _ func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error)) error {
	r.ids = append(r.ids, id)
	return nil
}

// Without the variable the two RPCs are not registered, so Nakama itself
// answers a call with "RPC function not found".
func TestDevSeedRPCs_AreRegisteredOnlyWithTheVariable(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", " 1"} {
		t.Setenv("MELLO_ENABLE_DEV_SEED", value)
		reg := &seedRegistrar{}
		if err := registerDevSeedRPCs(reg, testLogger()); err != nil {
			t.Fatal(err)
		}
		if len(reg.ids) != 0 {
			t.Errorf("MELLO_ENABLE_DEV_SEED=%q registered %v, want no RPC", value, reg.ids)
		}
	}

	t.Setenv("MELLO_ENABLE_DEV_SEED", "1")
	reg := &seedRegistrar{}
	if err := registerDevSeedRPCs(reg, testLogger()); err != nil {
		t.Fatal(err)
	}
	if len(reg.ids) != 2 || reg.ids[0] != "dev_seed_state" || reg.ids[1] != "dev_seed_chat" {
		t.Errorf("MELLO_ENABLE_DEV_SEED=1 registered %v, want dev_seed_state and dev_seed_chat", reg.ids)
	}
}
