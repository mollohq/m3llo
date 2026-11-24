package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"os"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The check of the server-to-server RPCs (specs/04-BACKEND.md section 4).
//
// A server-to-server RPC is called with the HTTP key and no user session.
// With MELLO_SERVER_RPC_SECRET set, the call must also carry that value in a
// request header. Only server-side callers hold the value. A client build
// does not, so a handler that a client calls must never use this check.

const (
	// serverRPCSecretEnv names the environment variable with the server
	// secret. Not set or empty: the header is not required.
	serverRPCSecretEnv = "MELLO_SERVER_RPC_SECRET"
	// serverRPCSecretHeader is the request header that carries the secret.
	serverRPCSecretHeader = "X-Mello-Server-Secret"
)

// requireServerCaller refuses a call that does not come from one of our own
// servers. It must be the first statement of the handler of a
// server-to-server RPC, and of no other handler: a handler that a client
// calls must never get it, because a client cannot send the server secret.
//
// It refuses a call with a user session. With serverRPCSecretEnv set, it also
// refuses a call that does not carry the same value in serverRPCSecretHeader.
func requireServerCaller(ctx context.Context, rpc string) error {
	if uid, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); uid != "" {
		return runtime.NewError(rpc+" is server-to-server only", 7) // PERMISSION_DENIED
	}
	want := strings.TrimSpace(os.Getenv(serverRPCSecretEnv))
	if want == "" {
		return nil
	}
	if !requestHasServerSecret(ctx, want) {
		return runtime.NewError(rpc+" needs the server secret", 7) // PERMISSION_DENIED
	}
	return nil
}

// requestHasServerSecret reports whether a request header named
// serverRPCSecretHeader holds want. Nakama gives the handler the headers of an
// HTTP call with their canonical names and the metadata of a gRPC call with
// lower case names, so the name is matched without case.
func requestHasServerSecret(ctx context.Context, want string) bool {
	headers, _ := ctx.Value(runtime.RUNTIME_CTX_HEADERS).(map[string][]string)
	wantSum := sha256.Sum256([]byte(want))
	found := false
	for name, values := range headers {
		if !strings.EqualFold(name, serverRPCSecretHeader) {
			continue
		}
		for _, value := range values {
			// Compare digests in constant time: the time of the compare tells
			// nothing about the secret, not even its length.
			gotSum := sha256.Sum256([]byte(strings.TrimSpace(value)))
			if subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) == 1 {
				found = true
			}
		}
	}
	return found
}
