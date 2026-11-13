package main

import (
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The characterisation tests of the clip, stream session and recap read
// paths (history_characterisation_test.go) run once for each mode of this
// list. A mode says where the crew history is read from. The tests and what
// they expect are the same in every mode.
type histMode struct {
	name string
	// setup runs before the test seeds anything.
	setup func(t *testing.T)
	// cutover runs after the test seeded its data and before it reads.
	cutover func(t *testing.T, nk runtime.NakamaModule)
}

// histModes are the modes. Phase 1b adds the modes that read the durable
// store.
var histModes = []histMode{
	{name: "document"},
}

// forHistModes runs fn once for each mode.
func forHistModes(t *testing.T, fn func(t *testing.T, m histMode)) {
	t.Helper()
	for _, m := range histModes {
		m := m
		t.Run(m.name, func(t *testing.T) {
			if m.setup != nil {
				m.setup(t)
			}
			fn(t, m)
		})
	}
}

// ready is called by a test when its seed data is written.
func (m histMode) ready(t *testing.T, nk runtime.NakamaModule) {
	t.Helper()
	if m.cutover != nil {
		m.cutover(t, nk)
	}
}
