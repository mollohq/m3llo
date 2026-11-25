package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"unicode/utf8"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// Client writes and deletes through the storage API of Nakama
// (specs/04-BACKEND.md section 6).
//
// The server writes its data through the runtime. A client writes two objects
// of its own through the storage API:
//
//   - avatars/current: the avatar of the user
//     (specs/features/AVATAR-SYSTEM.md section 5)
//   - chat_read/<crew id>: where the user stopped reading the chat of a crew
//     (specs/features/TEXT-CHAT.md section 2.5)
//
// BeforeWriteStorageObjects lets these two through after it checks the
// permissions, the size and the shape of the value. It refuses every other
// write. BeforeDeleteStorageObjects refuses every delete: no client deletes
// a storage object.
//
// The hooks run for a client call only (HTTP or gRPC). A write or a delete
// from the server runtime (nk.StorageWrite, nk.StorageDelete) does not pass
// them, so the RPCs and the jobs write as before. The console API does not
// pass them either. The realtime socket has no storage message.

const (
	// The avatar object of a user. The owner writes it with public read.
	clientAvatarCollection = "avatars"
	clientAvatarKey        = "current"
	// clientAvatarMaxBytes is the largest avatar value. It is the request
	// limit of the server (nakama.yml, max_request_size_bytes), so no write
	// that the server takes today is larger.
	clientAvatarMaxBytes = 128 << 10
	// clientAvatarTagMaxChars bounds the style and the seed of a generated
	// avatar. The avatar worker takes a seed of 128 characters at most.
	clientAvatarTagMaxChars = 128

	// The read cursor of a user in the chat of a crew. The key is the crew id.
	clientChatReadCollection = "chat_read"
	// clientChatCursorMaxBytes bounds the cursor value: a message id and a
	// time.
	clientChatCursorMaxBytes = 256
	// clientChatCursorFieldMaxChars bounds each field of the cursor.
	clientChatCursorFieldMaxChars = 64

	// clientStorageWriteMaxObjects is the number of objects in one client
	// write. A client writes one object in a request.
	clientStorageWriteMaxObjects = 1

	// Nakama permission values of a storage object.
	storagePermissionOwnerRead  = 1
	storagePermissionPublicRead = 2
	storagePermissionOwnerWrite = 1
)

var (
	errStorageWriteRefused   = runtime.NewError("storage write refused", 7)
	errStorageDeleteRefused  = runtime.NewError("storage delete refused", 7)
	errStorageWriteNotMember = runtime.NewError("not a crew member", 7)
	errAvatarValue           = runtime.NewError("avatar value not accepted", 3)
	errChatCursorValue       = runtime.NewError("chat read cursor value not accepted", 3)
)

// BeforeWriteStorageObjects runs before a client write. It refuses the full
// request when one object of it is not allowed.
func BeforeWriteStorageObjects(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.WriteStorageObjectsRequest) (*api.WriteStorageObjectsRequest, error) {
	callerID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if callerID == "" {
		return nil, errStorageWriteRefused
	}
	objects := in.GetObjects()
	if len(objects) == 0 {
		// Nakama answers an empty request with an empty list.
		return in, nil
	}
	if len(objects) > clientStorageWriteMaxObjects {
		logger.Warn("storage write refused: %d objects in one request, caller=%s", len(objects), callerID)
		return nil, errStorageWriteRefused
	}
	for _, o := range objects {
		if err := clientMayWriteObject(ctx, nk, callerID, o); err != nil {
			// The key is not in the log: a client chooses its text.
			logger.Warn("storage write refused: collection=%q caller=%s: %v", logText(o.GetCollection()), callerID, err)
			return nil, err
		}
	}
	return in, nil
}

// BeforeDeleteStorageObjects runs before a client delete. It refuses every
// delete. The account delete of Nakama removes the objects of a user, and
// the RPCs delete through the runtime.
func BeforeDeleteStorageObjects(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.DeleteStorageObjectsRequest) (*api.DeleteStorageObjectsRequest, error) {
	callerID, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	logger.Warn("storage delete refused: %d objects, caller=%s", len(in.GetObjectIds()), callerID)
	return nil, errStorageDeleteRefused
}

// clientMayWriteObject says if a session of callerID may write one object
// through the storage API. Nil means yes. The owner of a client write is the
// caller: Nakama sets it, and the request has no owner field.
func clientMayWriteObject(ctx context.Context, nk runtime.NakamaModule, callerID string, o *api.WriteStorageObject) error {
	if o == nil {
		return errStorageWriteRefused
	}
	switch o.GetCollection() {
	case clientAvatarCollection:
		if o.GetKey() != clientAvatarKey {
			return errStorageWriteRefused
		}
		if !hasStoragePermissions(o, storagePermissionPublicRead, storagePermissionOwnerWrite) {
			return errStorageWriteRefused
		}
		if !validAvatarValue(o.GetValue()) {
			return errAvatarValue
		}
		return nil
	case clientChatReadCollection:
		if !isUUIDText(o.GetKey()) {
			return errStorageWriteRefused
		}
		if !hasStoragePermissions(o, storagePermissionOwnerRead, storagePermissionOwnerWrite) {
			return errStorageWriteRefused
		}
		if !validChatCursorValue(o.GetValue()) {
			return errChatCursorValue
		}
		// The cursor is of a crew that the caller is in. A failed member
		// read counts as no membership.
		if !isCrewMember(ctx, nk, o.GetKey(), callerID) {
			return errStorageWriteNotMember
		}
		return nil
	}
	return errStorageWriteRefused
}

// hasStoragePermissions says if the write names both permissions and they
// are the given ones. A write that names none gets the defaults of Nakama,
// so it is not accepted.
func hasStoragePermissions(o *api.WriteStorageObject, read, write int32) bool {
	if o.GetPermissionRead() == nil || o.GetPermissionWrite() == nil {
		return false
	}
	return o.GetPermissionRead().GetValue() == read && o.GetPermissionWrite().GetValue() == write
}

// stringFields decodes a JSON object whose every field is a string and whose
// field names are all in allowed. It returns false for any other value.
func stringFields(value string, allowed ...string) (map[string]string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &raw); err != nil || raw == nil {
		return nil, false
	}
	out := make(map[string]string, len(raw))
	for name, field := range raw {
		known := false
		for _, a := range allowed {
			if name == a {
				known = true
				break
			}
		}
		if !known {
			return nil, false
		}
		// A JSON null decodes into a string with no error. It is not a string.
		if len(field) == 0 || field[0] != '"' {
			return nil, false
		}
		var text string
		if err := json.Unmarshal(field, &text); err != nil {
			return nil, false
		}
		out[name] = text
	}
	return out, true
}

// validAvatarValue checks the avatar value that the clients write:
// {"format": "svg" or "png", "data": text, "style": text, "seed": text}.
// The style and the seed are optional. The value has no other field.
func validAvatarValue(value string) bool {
	if len(value) > clientAvatarMaxBytes {
		return false
	}
	fields, ok := stringFields(value, "format", "data", "style", "seed")
	if !ok {
		return false
	}
	if format := fields["format"]; format != "svg" && format != "png" {
		return false
	}
	if fields["data"] == "" {
		return false
	}
	for _, name := range []string{"style", "seed"} {
		if utf8.RuneCountInString(fields[name]) > clientAvatarTagMaxChars {
			return false
		}
	}
	return true
}

// validChatCursorValue checks the cursor value that the clients write:
// {"message_id": text, "time": text}. Both fields are there. The value has
// no other field.
func validChatCursorValue(value string) bool {
	if len(value) > clientChatCursorMaxBytes {
		return false
	}
	fields, ok := stringFields(value, "message_id", "time")
	if !ok {
		return false
	}
	for _, name := range []string{"message_id", "time"} {
		text, present := fields[name]
		if !present || utf8.RuneCountInString(text) > clientChatCursorFieldMaxChars {
			return false
		}
	}
	return true
}

// isUUIDText says if s has the text form of a UUID: 8-4-4-4-12 hex digits.
func isUUIDText(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// logText cuts a text of a client to a length that is safe in a log line.
func logText(s string) string {
	const max = 64
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// registerStorageWriteHooks registers the two hooks.
func registerStorageWriteHooks(initializer runtime.Initializer) error {
	if err := initializer.RegisterBeforeWriteStorageObjects(BeforeWriteStorageObjects); err != nil {
		return err
	}
	return initializer.RegisterBeforeDeleteStorageObjects(BeforeDeleteStorageObjects)
}
