package main

import (
	"os"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The dev seed RPCs (dev_seed_state, dev_seed_chat) rewrite the data of the
// users and crews with the seed names, whoever owns them. They exist only on
// an install that sets MELLO_ENABLE_DEV_SEED=1: a local stack or a test
// stack. Never set the variable on an install that other people can reach.
//
// The gate has two parts. main.go registers the two RPCs only when the seed
// is on (registerDevSeedRPCs), so on every other install Nakama itself
// answers a call with "RPC function not found". Each handler also checks the
// variable before it reads or writes anything.

// devSeedEnv is the variable that turns the dev seed on. Only the value 1
// does that.
const devSeedEnv = "MELLO_ENABLE_DEV_SEED"

// devSeedEnabled reports whether this install turned the dev seed on.
func devSeedEnabled() bool {
	return os.Getenv(devSeedEnv) == "1"
}

// errDevSeedOff is the answer of a seed RPC when the seed is off: the code
// and the text that Nakama gives for an RPC that does not exist.
var errDevSeedOff = runtime.NewError("RPC function not found", 5)
