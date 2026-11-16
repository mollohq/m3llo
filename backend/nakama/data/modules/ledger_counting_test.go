package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// queryCounter counts the calls to a store and the rows its reads return.
// It names every method of ledgerStore, so a new method of the interface does
// not pass it without a count.
type queryCounter struct {
	inner ledgerStore
	mu    sync.Mutex
	calls map[string]int
	rows  int
}

var _ ledgerStore = (*queryCounter)(nil)

func newQueryCounter(inner ledgerStore) *queryCounter {
	return &queryCounter{inner: inner, calls: map[string]int{}}
}

func (c *queryCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls, c.rows = map[string]int{}, 0
}

func (c *queryCounter) call(name string, rows int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[name]++
	c.rows += rows
}

// total is the number of calls and the number of rows read since the reset.
func (c *queryCounter) total() (queries, rows int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.calls {
		queries += n
	}
	return queries, c.rows
}

// String is the calls by method, sorted: "Between=2 NewestN=1".
func (c *queryCounter) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.calls))
	for name := range c.calls {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, c.calls[name]))
	}
	return strings.Join(parts, " ")
}

func (c *queryCounter) events(name string, events []CrewEvent, err error) ([]CrewEvent, error) {
	c.call(name, len(events))
	return events, err
}

func (c *queryCounter) Append(ctx context.Context, ev CrewEvent, ref string) (bool, error) {
	c.call("Append", 0)
	return c.inner.Append(ctx, ev, ref)
}

func (c *queryCounter) Backfill(ctx context.Context, crewID string, docEvents []CrewEvent, anchorMs int64, docOrderWins bool) (int, error) {
	c.call("Backfill", 0)
	return c.inner.Backfill(ctx, crewID, docEvents, anchorMs, docOrderWins)
}

func (c *queryCounter) UpdateData(ctx context.Context, crewID, eventID string, fn func(json.RawMessage) (json.RawMessage, error)) error {
	c.call("UpdateData", 0)
	return c.inner.UpdateData(ctx, crewID, eventID, fn)
}

func (c *queryCounter) Delete(ctx context.Context, crewID, eventID string) error {
	c.call("Delete", 0)
	return c.inner.Delete(ctx, crewID, eventID)
}

func (c *queryCounter) Prune(ctx context.Context, crewID string, eventIDs []string) error {
	c.call("Prune", 0)
	return c.inner.Prune(ctx, crewID, eventIDs)
}

func (c *queryCounter) Between(ctx context.Context, crewID string, fromMs, toMs int64, types []string) ([]CrewEvent, error) {
	events, err := c.inner.Between(ctx, crewID, fromMs, toMs, types)
	return c.events("Between", events, err)
}

func (c *queryCounter) NewestN(ctx context.Context, crewID string, types []string, beforeMs int64, n int) ([]CrewEvent, error) {
	events, err := c.inner.NewestN(ctx, crewID, types, beforeMs, n)
	return c.events("NewestN", events, err)
}

func (c *queryCounter) CountOlder(ctx context.Context, crewID string, types []string, beforeMs int64) (int, error) {
	c.call("CountOlder", 0)
	return c.inner.CountOlder(ctx, crewID, types, beforeMs)
}

func (c *queryCounter) WeeksBefore(ctx context.Context, crewID string, types []string, beforeWeek int64, limit int) ([]weekCount, error) {
	c.call("WeeksBefore", 0)
	return c.inner.WeeksBefore(ctx, crewID, types, beforeWeek, limit)
}

func (c *queryCounter) RecentView(ctx context.Context, crewID string) ([]CrewEvent, error) {
	events, err := c.inner.RecentView(ctx, crewID)
	return c.events("RecentView", events, err)
}

func (c *queryCounter) ByType(ctx context.Context, typ string, sinceMs int64) ([]CrewEvent, error) {
	events, err := c.inner.ByType(ctx, typ, sinceMs)
	return c.events("ByType", events, err)
}

func (c *queryCounter) DeleteCrew(ctx context.Context, crewID string) error {
	c.call("DeleteCrew", 0)
	return c.inner.DeleteCrew(ctx, crewID)
}

func (c *queryCounter) EventsNamingUser(ctx context.Context, userID string) ([]CrewEvent, error) {
	events, err := c.inner.EventsNamingUser(ctx, userID)
	return c.events("EventsNamingUser", events, err)
}

func (c *queryCounter) DeletedIDs(ctx context.Context, crewID string, ids []string) (map[string]bool, error) {
	c.call("DeletedIDs", 0)
	return c.inner.DeletedIDs(ctx, crewID, ids)
}

func (c *queryCounter) UserCrews(ctx context.Context, userID string) ([]string, error) {
	c.call("UserCrews", 0)
	return c.inner.UserCrews(ctx, userID)
}

func (c *queryCounter) NamingUserInCrew(ctx context.Context, userID, crewID string, n int) ([]CrewEvent, error) {
	events, err := c.inner.NamingUserInCrew(ctx, userID, crewID, n)
	return c.events("NamingUserInCrew", events, err)
}

func (c *queryCounter) ForgetUser(ctx context.Context, userID, crewID string, eventIDs []string) error {
	c.call("ForgetUser", 0)
	return c.inner.ForgetUser(ctx, userID, crewID, eventIDs)
}

func (c *queryCounter) Put(ctx context.Context, crewID string, rows []ledgerRow) (int, error) {
	c.call("Put", 0)
	return c.inner.Put(ctx, crewID, rows)
}

func (c *queryCounter) PutUnique(ctx context.Context, crewID string, row ledgerRow) ([]CrewEvent, error) {
	c.call("PutUnique", 0)
	return c.inner.PutUnique(ctx, crewID, row)
}

func (c *queryCounter) ByRef(ctx context.Context, crewID, typ, ref string) ([]CrewEvent, error) {
	events, err := c.inner.ByRef(ctx, crewID, typ, ref)
	return c.events("ByRef", events, err)
}

func (c *queryCounter) ByIDs(ctx context.Context, crewID string, ids []string) ([]CrewEvent, error) {
	events, err := c.inner.ByIDs(ctx, crewID, ids)
	return c.events("ByIDs", events, err)
}

func (c *queryCounter) ByRefs(ctx context.Context, crewID, typ string, refs []string) ([]CrewEvent, error) {
	events, err := c.inner.ByRefs(ctx, crewID, typ, refs)
	return c.events("ByRefs", events, err)
}

func (c *queryCounter) RefsBefore(ctx context.Context, crewID, typ, beforeRef string, n int) ([]CrewEvent, error) {
	events, err := c.inner.RefsBefore(ctx, crewID, typ, beforeRef, n)
	return c.events("RefsBefore", events, err)
}

func (c *queryCounter) TopInRange(ctx context.Context, crewID, typ string, fromMs, toMs int64, field, value string, n int) ([]CrewEvent, error) {
	events, err := c.inner.TopInRange(ctx, crewID, typ, fromMs, toMs, field, value, n)
	return c.events("TopInRange", events, err)
}
