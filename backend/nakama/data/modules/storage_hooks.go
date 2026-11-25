package main

import (
	"context"
	"database/sql"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Client access to the storage API of Nakama (specs/04-BACKEND.md section 6).
//
// A client gets crew data from the RPCs. These two hooks close the storage
// API to clients, apart from the two reads that the clients make:
//
//   - avatars/current of a user (the user avatars)
//   - an object that the caller owns (the chat read cursors, chat_read)
//
// The rule does not look at the stored permission of an object, so it holds
// for every object, whatever read permission a write gives it. Nakama still
// applies the stored permission to a read that the hook lets through.
//
// The hooks run for a client call only (HTTP or gRPC). A read from the server
// runtime (nk.StorageRead, nk.StorageList) does not pass them, so the RPCs
// and the jobs read as before. The console API does not pass them either.

const (
	// UserAvatarCollection and UserAvatarKey name the avatar object of a
	// user. The client writes it and reads it for other users
	// (specs/features/AVATAR-SYSTEM.md section 5).
	UserAvatarCollection = "avatars"
	UserAvatarKey        = "current"
)

var (
	errStorageReadRefused = runtime.NewError("storage read refused", 7)
	errStorageListRefused = runtime.NewError("storage list refused", 7)
)

// clientMayReadObject says if a session of callerID may read one object
// through the storage API. An empty owner is the system user.
func clientMayReadObject(callerID string, id *api.ReadStorageObjectId) bool {
	if callerID == "" || id == nil {
		return false
	}
	owner := id.GetUserId()
	if owner == "" {
		return false
	}
	if owner == callerID {
		return true
	}
	return id.GetCollection() == UserAvatarCollection && id.GetKey() == UserAvatarKey
}

// BeforeReadStorageObjects runs before a client read by key. It refuses the
// full request when one object of it is not allowed, so a client sees the
// refusal and does not take a missing object for an empty one.
func BeforeReadStorageObjects(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ReadStorageObjectsRequest) (*api.ReadStorageObjectsRequest, error) {
	callerID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	for _, id := range in.GetObjectIds() {
		if !clientMayReadObject(callerID, id) {
			// The key can be an invite code. It stays out of the log.
			logger.Warn("storage read refused: collection=%q caller=%s", id.GetCollection(), callerID)
			return nil, errStorageReadRefused
		}
	}
	return in, nil
}

// BeforeListStorageObjects runs before a client list of a collection. It
// refuses every list. No client lists a collection.
func BeforeListStorageObjects(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ListStorageObjectsRequest) (*api.ListStorageObjectsRequest, error) {
	callerID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	logger.Warn("storage list refused: collection=%q caller=%s", in.GetCollection(), callerID)
	return nil, errStorageListRefused
}
