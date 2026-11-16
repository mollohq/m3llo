package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
)

// Dual write to the legacy ledger document and the durable ledger store
// (specs/16-CREW-EVENT-LEDGER.md section 3.8).
//
// One of the two is the truth: the one the readers use.
//   - Before the cutover it is the document. The document is written first.
//     When that fails the caller gets the error and the store is not written.
//     A store failure is logged, and the reconcile repairs it from the document.
//   - After the cutover it is the store. The store is written first. When
//     that fails the caller gets the error and the document is not written.
//     A document failure is logged only.
//
// With no store set, only the document is written.

func ledgerLogError(format string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Error(format, args...)
	}
}

// ledgerWriteBoth runs one write of the legacy ledger path on both stores in
// the order of the phase. op names the write in the log lines.
func ledgerWriteBoth(ctx context.Context, nk runtime.NakamaModule, op, crewID, eventID string, toStore func(ledgerStore) error, toDoc func() error) error {
	return ledgerWriteOrdered(ledgerTruthStore(ctx, nk), op, crewID, eventID, toStore, toDoc)
}

// historyWriteBoth is ledgerWriteBoth for the clips and the weekly recaps of
// phase 1b. Their cutover has its own marker (historyTruthStore).
func historyWriteBoth(ctx context.Context, nk runtime.NakamaModule, op, crewID, eventID string, toStore func(ledgerStore) error, toDoc func() error) error {
	return ledgerWriteOrdered(historyTruthStore(ctx, nk), op, crewID, eventID, toStore, toDoc)
}

// ledgerWriteOrdered runs one write on both stores. truth is the durable
// store when it is the truth for this write, and nil when the legacy document
// is. The truth is written first and its error is returned.
func ledgerWriteOrdered(truth ledgerStore, op, crewID, eventID string, toStore func(ledgerStore) error, toDoc func() error) error {
	if store := truth; store != nil {
		if err := toStore(store); err != nil {
			ledgerLogError("ledger_store_write_failed op=%s crew=%s event=%s: %v", op, crewID, eventID, err)
			return fmt.Errorf("ledger %s failed for crew %s event %s: %w", op, crewID, eventID, err)
		}
		if err := toDoc(); err != nil {
			ledgerLogError("ledger_doc_write_failed op=%s crew=%s event=%s: %v", op, crewID, eventID, err)
		}
		return nil
	}
	if err := toDoc(); err != nil {
		return err
	}
	if store := currentLedger(); store != nil {
		if err := toStore(store); err != nil && !errors.Is(err, errLedgerEventNotFound) {
			// The reconcile copies the document state to the store.
			ledgerLogError("ledger_store_write_failed op=%s crew=%s event=%s: %v. The reconcile repairs it", op, crewID, eventID, err)
		}
	}
	return nil
}

// ledgerStoreEvent is an event of the legacy ledger path as the store keeps
// it: with its crew id, and with a "ledger_" type when the type is not a
// pulse type (ledgerStoredType).
func ledgerStoreEvent(crewID string, event CrewEvent) CrewEvent {
	event.CrewID = crewID
	event.Type = ledgerStoredType(event.Type)
	return event
}

// ledgerStoreAppend writes one event of the legacy ledger path to the store.
func ledgerStoreAppend(ctx context.Context, store ledgerStore, crewID string, event CrewEvent) error {
	_, err := store.Append(ctx, ledgerStoreEvent(crewID, event), eventRef(event))
	return err
}

// ledgerStoreSetSnapshotURLs sets the snapshot URLs of a stream_session event
// in the store.
func ledgerStoreSetSnapshotURLs(ctx context.Context, store ledgerStore, crewID, eventID string, snapshotURLs []string) error {
	return store.UpdateData(ctx, crewID, eventID, func(raw json.RawMessage) (json.RawMessage, error) {
		data, err := decodeStreamSessionData([]byte(raw))
		if err != nil {
			return nil, err
		}
		data.SnapshotURLs = snapshotURLs
		return json.Marshal(data)
	})
}
