package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The chat of a deleted crew (specs/16-CREW-EVENT-LEDGER.md section 10.3). The
// purge of a crew removes:
//
//   - every message of the group channel of the crew. Nakama keeps them in
//     its message table. A group delete of Nakama leaves them there;
//   - the reactions of the crew (chat_reactions/{message_id}, with the crew
//     id in the document);
//   - the chat images, with the other media ({crew_id}/ in the media bucket,
//     crewMediaPrefixes).
//
// The runtime of Nakama has no call that deletes the messages of a channel.
// ChannelMessageRemove deletes one message: it needs the sender of each
// message, runs one statement for each, and sends one event to the channel
// for each. So the purge uses SQL on the message table, as dev_seed_chat
// does, with placeholders and a bounded batch.
//
// A reactions document has the id of its message as key, and nothing lists
// the documents of a crew. The purge reads their keys from the storage table
// with SQL, by the crew id in the document. The read looks at every reactions
// document of the install: a purge is rare. The delete goes through the
// storage call of Nakama.
//
// A crew has one text channel: the group channel of Nakama (stream mode 3,
// the subject is the group id). A voice channel has no text. A direct message
// has stream mode 4 and two user ids. Every statement here names stream mode
// 3 and the crew id, so no other channel can match.

const (
	// chatStreamModeGroup is the stream mode of a group channel in the
	// message table of Nakama.
	chatStreamModeGroup = 3
)

// crewPurgeChatBatch is the number of messages of one batch. A var so a test
// makes it small.
var crewPurgeChatBatch = 200

// crewChatStore is what the purge needs of the message table.
type crewChatStore interface {
	// MessageIDs returns the ids of up to limit messages of the group
	// channel of the crew, the oldest first.
	MessageIDs(ctx context.Context, crewID string, limit int) ([]string, error)
	// DeleteMessages deletes the messages with these ids that are in the
	// group channel of the crew. It returns the number of deleted messages.
	DeleteMessages(ctx context.Context, crewID string, ids []string) (int, error)
	// ReactionKeys returns the keys of up to limit reactions documents of
	// the crew, in key order.
	ReactionKeys(ctx context.Context, crewID string, limit int) ([]string, error)
}

// crewChat returns the message table of this install, or nil when the module
// has no database handle. A var so a test gives a fake.
var crewChat = func() crewChatStore {
	if globalDb == nil {
		return nil
	}
	return sqlCrewChat{db: globalDb}
}

type sqlCrewChat struct {
	db *sql.DB
}

func (s sqlCrewChat) MessageIDs(ctx context.Context, crewID string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM message WHERE stream_mode = $1 AND stream_subject = $2::UUID ORDER BY create_time, id LIMIT $3`,
		chatStreamModeGroup, crewID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s sqlCrewChat) DeleteMessages(ctx context.Context, crewID string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	// One placeholder for each id. The stream mode and the crew id are in the
	// statement again: an id of another channel deletes nothing.
	args := make([]interface{}, 0, len(ids)+2)
	args = append(args, chatStreamModeGroup, crewID)
	marks := make([]string, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
		marks = append(marks, fmt.Sprintf("$%d::UUID", len(args)))
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM message WHERE stream_mode = $1 AND stream_subject = $2::UUID AND id IN (`+strings.Join(marks, ", ")+`)`,
		args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s sqlCrewChat) ReactionKeys(ctx context.Context, crewID string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT key FROM storage WHERE collection = $1 AND user_id = $2::UUID AND value->>'crew_id' = $3 ORDER BY key LIMIT $4`,
		ChatReactionsCollection, SystemUserID, crewID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// crewHasChat reports whether the message table holds a message of the crew.
func crewHasChat(ctx context.Context, crewID string) (bool, error) {
	chat := crewChat()
	if chat == nil {
		return false, nil
	}
	ids, err := chat.MessageIDs(ctx, crewID, 1)
	return len(ids) > 0, err
}

// purgeCrewChat deletes the chat messages of a crew and the reactions of the
// crew, a batch at a time, until none is left or the deadline passes.
//
// The reactions are found by the crew id in the document, not by the id of a
// message: the reactions of a message that is gone already go too. Nakama
// deletes the messages of an account with the account, so the reactions of
// those messages have no message.
func purgeCrewChat(ctx context.Context, nk runtime.NakamaModule, res *crewPurgeResult, crewID string, deadline time.Time) {
	if !validPurgeCrewID(crewID) {
		res.fail("chat: the crew id is not a group id")
		return
	}
	chat := crewChat()
	if chat == nil {
		res.fail("chat: the module has no database handle")
		return
	}
	for {
		if crewPurgeNow().After(deadline) {
			res.Incomplete = true
			return
		}
		ids, err := chat.MessageIDs(ctx, crewID, crewPurgeChatBatch)
		if err != nil {
			res.fail("chat read: %v", err)
			return
		}
		if len(ids) == 0 {
			break
		}
		n, err := chat.DeleteMessages(ctx, crewID, ids)
		res.ChatDeleted += n
		if err != nil {
			res.fail("chat delete: %v", err)
			return
		}
		if n == 0 {
			// The same ids would come back for ever.
			res.fail("chat delete: no message of the batch was deleted")
			return
		}
	}
	last := ""
	for {
		if crewPurgeNow().After(deadline) {
			res.Incomplete = true
			return
		}
		keys, err := chat.ReactionKeys(ctx, crewID, crewPurgeChatBatch)
		if err != nil {
			res.fail("%s read: %v", ChatReactionsCollection, err)
			return
		}
		if len(keys) == 0 {
			return
		}
		if keys[0] == last {
			res.fail("%s delete: no document of the batch was deleted", ChatReactionsCollection)
			return
		}
		last = keys[0]
		deletes := make([]*runtime.StorageDelete, 0, len(keys))
		for _, key := range keys {
			deletes = append(deletes, &runtime.StorageDelete{Collection: ChatReactionsCollection, Key: key, UserID: SystemUserID})
		}
		if err := nk.StorageDelete(ctx, deletes); err != nil {
			res.fail("%s delete: %v", ChatReactionsCollection, err)
			return
		}
		res.ReactionsDeleted += len(deletes)
	}
}
