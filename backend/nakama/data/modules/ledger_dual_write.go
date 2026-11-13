package main

import (
	"context"
	"encoding/json"
	"errors"
)

// Dual write to the durable ledger store (plans/DURABLE-LEDGER.md section 5).
// Each helper is a no-op when the store is not set. A store error is logged
// and never fails the caller: the legacy document is still written, and the
// backfill of the next start copies what the store missed.

func ledgerLogError(format string, args ...interface{}) {
	if globalLogger != nil {
		globalLogger.Error(format, args...)
	}
}

// ledgerStoreAppend writes one event to the durable store.
func ledgerStoreAppend(ctx context.Context, crewID string, event CrewEvent) {
	if crewLedger == nil {
		return
	}
	event.CrewID = crewID
	if _, err := crewLedger.Append(ctx, event, eventRef(event)); err != nil {
		ledgerLogError("ledger_store_append_failed crew=%s event=%s type=%s: %v", crewID, event.ID, event.Type, err)
	}
}

// ledgerStoreSetSnapshotURLs sets the snapshot URLs of a stream_session event
// in the durable store. An event the store does not hold yet is not an error:
// the backfill copies it from the document with its URLs.
func ledgerStoreSetSnapshotURLs(ctx context.Context, crewID, eventID string, snapshotURLs []string) {
	if crewLedger == nil {
		return
	}
	err := crewLedger.UpdateData(ctx, crewID, eventID, func(raw json.RawMessage) (json.RawMessage, error) {
		data, err := decodeStreamSessionData([]byte(raw))
		if err != nil {
			return nil, err
		}
		data.SnapshotURLs = snapshotURLs
		return json.Marshal(data)
	})
	if err != nil && !errors.Is(err, errLedgerEventNotFound) {
		ledgerLogError("ledger_store_update_failed crew=%s event=%s: %v", crewID, eventID, err)
	}
}

// ledgerStoreDelete removes one event from the durable store.
func ledgerStoreDelete(ctx context.Context, crewID, eventID string) {
	if crewLedger == nil {
		return
	}
	if err := crewLedger.Delete(ctx, crewID, eventID); err != nil {
		ledgerLogError("ledger_store_delete_failed crew=%s event=%s: %v", crewID, eventID, err)
	}
}
