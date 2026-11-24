package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"regexp"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Client access to the built-in API of Nakama and to the realtime socket
// (specs/04-BACKEND.md section 7).
//
// A client gets crew data and changes a crew through the RPCs. Of the
// built-in API it uses a small part: the account calls, the crews of the
// caller, the join of an open crew, the member list and the chat of a crew
// of the caller, and the name of a user by id. These hooks keep that part
// and refuse the rest.
//
// The hooks run for a client call only (HTTP, gRPC or the socket). A call
// from the server runtime (nk.GroupUsersAdd, nk.GroupUsersKick and so on)
// does not pass them, so the RPCs and the jobs work as before. The console
// API does not pass them either.

var (
	errBuiltinCallRefused = runtime.NewError("this call is not available", 7)
	errBuiltinNotAMember  = runtime.NewError("not a crew member", 7)
	errBuiltinInviteOnly  = runtime.NewError("this crew takes members by invite code", 7)
	errBuiltinInternal    = runtime.NewError("internal error", 13)
)

var builtinUUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// builtinNoCrewName is a group name that no crew has. A list by name that
// must find nothing gets this name, so its answer is the same as for a name
// that does not exist. It is random for each start of the module.
var builtinNoCrewName = func() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic("no random bytes for the group name that matches nothing: " + err.Error())
	}
	return "no-crew-" + hex.EncodeToString(b)
}()

func builtinCaller(ctx context.Context) string {
	id, _ := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	return id
}

// builtinIsMember says if the user is the owner, an admin or a member of the
// crew. A join request and a ban are rows too, and are not a membership.
func builtinIsMember(ctx context.Context, nk runtime.NakamaModule, userID, crewID string) (bool, error) {
	state, err := callerCrewState(ctx, nk, userID, crewID)
	if err != nil {
		return false, err
	}
	return state != nil && *state >= groupStateSuperadmin && *state <= groupStateMember, nil
}

// ---------------------------------------------------------------------------
// Groups
// ---------------------------------------------------------------------------

// BeforeJoinGroup runs before the built-in join. The client joins an open
// crew of Discover with it. An invite-only crew takes members through
// join_by_invite_code, so the built-in join of it is refused and makes no
// join request row.
func BeforeJoinGroup(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.JoinGroupRequest) (*api.JoinGroupRequest, error) {
	callerID := builtinCaller(ctx)
	if callerID == "" {
		return nil, errBuiltinCallRefused
	}
	if !builtinUUID.MatchString(in.GetGroupId()) {
		// Nakama answers for an id that is not an id.
		return in, nil
	}
	groups, err := nk.GroupsGetId(ctx, []string{in.GetGroupId()})
	if err != nil {
		logger.Error("built-in join: crew %s read failed: %v", in.GetGroupId(), err)
		return nil, errBuiltinInternal
	}
	if len(groups) == 0 {
		// Nakama answers for a crew that does not exist.
		return in, nil
	}
	if !groups[0].GetOpen().GetValue() {
		logger.Info("built-in join refused: crew %s is invite only, caller=%s", in.GetGroupId(), callerID)
		return nil, errBuiltinInviteOnly
	}
	return in, nil
}

// BeforeListGroupUsers runs before the built-in member list. Only a member of
// the crew gets it. The answer is the same for a crew that does not exist.
func BeforeListGroupUsers(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ListGroupUsersRequest) (*api.ListGroupUsersRequest, error) {
	callerID := builtinCaller(ctx)
	if callerID == "" {
		return nil, errBuiltinNotAMember
	}
	member, err := builtinIsMember(ctx, nk, callerID, in.GetGroupId())
	if err != nil {
		logger.Error("built-in member list: crews of %s read failed: %v", callerID, err)
		return nil, errBuiltinInternal
	}
	if !member {
		logger.Info("built-in member list refused: crew=%s caller=%s", in.GetGroupId(), callerID)
		return nil, errBuiltinNotAMember
	}
	return in, nil
}

// BeforeListUserGroups runs before the built-in list of the crews of a user.
// A caller lists the own crews only.
func BeforeListUserGroups(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ListUserGroupsRequest) (*api.ListUserGroupsRequest, error) {
	callerID := builtinCaller(ctx)
	if callerID == "" || in.GetUserId() != callerID {
		logger.Info("built-in crew list refused: caller=%s", callerID)
		return nil, errBuiltinCallRefused
	}
	return in, nil
}

// likeEscaper makes the wildcards of the name filter plain characters.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// BeforeListGroups runs before the built-in group list.
//
// A list with no name gives open crews only: the hook sets the open filter.
//
// A list by name finds a crew by its full name, without regard to case. It
// finds the crew when the crew is open or when the caller is a member of it.
// In every other case the list finds nothing, as for a name that no crew
// has. Nakama takes no other filter together with a name, so the hook reads
// the crews of that name itself.
func BeforeListGroups(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ListGroupsRequest) (*api.ListGroupsRequest, error) {
	callerID := builtinCaller(ctx)
	if callerID == "" {
		return nil, errBuiltinCallRefused
	}
	if in.GetName() == "" {
		in.Open = wrapperspb.Bool(true)
		return in, nil
	}

	name := likeEscaper.Replace(in.GetName())
	matches, _, err := nk.GroupsList(ctx, name, "", nil, nil, 100, "")
	if err != nil {
		logger.Error("built-in group list: crews by name read failed: %v", err)
		return nil, errBuiltinInternal
	}
	in.Name = name
	for _, g := range matches {
		if g.GetOpen().GetValue() {
			continue
		}
		member, err := builtinIsMember(ctx, nk, callerID, g.GetId())
		if err != nil {
			logger.Error("built-in group list: crews of %s read failed: %v", callerID, err)
			return nil, errBuiltinInternal
		}
		if !member {
			in.Name = builtinNoCrewName
			break
		}
	}
	return in, nil
}

// ---------------------------------------------------------------------------
// Users and chat history
// ---------------------------------------------------------------------------

// BeforeGetUsers runs before the built-in user lookup. The client asks for
// the name of a user by id. A lookup by username or by a social id is
// refused.
func BeforeGetUsers(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.GetUsersRequest) (*api.GetUsersRequest, error) {
	if len(in.GetUsernames()) > 0 || len(in.GetFacebookIds()) > 0 {
		logger.Info("built-in user lookup refused: not by id, caller=%s", builtinCaller(ctx))
		return nil, errBuiltinCallRefused
	}
	return in, nil
}

// builtinStripSocialIDs removes from a user the ids of the sign-in providers.
// The client reads the id, the names, the metadata and the online flag of a
// user. It reads no provider id.
func builtinStripSocialIDs(u *api.User) {
	if u == nil {
		return
	}
	u.AppleId = ""
	u.GoogleId = ""
	u.SteamId = ""
	u.FacebookId = ""
	u.FacebookInstantGameId = ""
	u.GamecenterId = ""
}

// AfterGetUsers runs after the built-in user lookup, before the answer goes
// to the client. It removes the provider ids from every user of the answer.
func AfterGetUsers(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Users, in *api.GetUsersRequest) error {
	for _, u := range out.GetUsers() {
		builtinStripSocialIDs(u)
	}
	return nil
}

// AfterListGroupUsers runs after the built-in member list, before the answer
// goes to the client. It removes the provider ids from every member.
func AfterListGroupUsers(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.GroupUserList, in *api.ListGroupUsersRequest) error {
	for _, gu := range out.GetGroupUsers() {
		builtinStripSocialIDs(gu.GetUser())
	}
	return nil
}

// builtinGroupChannelPrefix starts the id of the chat channel of a crew
// ("3.<crew id>.."). A room starts with "2." and a direct message with "4.".
const builtinGroupChannelPrefix = "3."

// BeforeListChannelMessages runs before the built-in message history. Only
// the chat of a crew has a history for clients. Nakama then checks that the
// caller is a member of that crew.
func BeforeListChannelMessages(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ListChannelMessagesRequest) (*api.ListChannelMessagesRequest, error) {
	if !strings.HasPrefix(in.GetChannelId(), builtinGroupChannelPrefix) {
		logger.Info("built-in message history refused: not a crew channel, caller=%s", builtinCaller(ctx))
		return nil, errBuiltinCallRefused
	}
	return in, nil
}

// ---------------------------------------------------------------------------
// Realtime socket
// ---------------------------------------------------------------------------

// BeforeRtChannelJoin runs before a channel join on the socket. The client
// joins the chat of a crew. A room and a direct message are refused. Nakama
// then checks that the caller is a member of that crew.
func BeforeRtChannelJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *rtapi.Envelope) (*rtapi.Envelope, error) {
	join := in.GetChannelJoin()
	if join == nil || join.GetType() != int32(rtapi.ChannelJoin_GROUP) {
		logger.Info("channel join refused: not a crew channel, caller=%s", builtinCaller(ctx))
		return nil, errBuiltinCallRefused
	}
	return in, nil
}

// builtinCrewMates returns which of the wanted users share a crew with the
// caller: both are the owner, an admin or a member of it. It stops when it
// has found them all.
func builtinCrewMates(ctx context.Context, nk runtime.NakamaModule, callerID string, wanted map[string]bool) (map[string]bool, error) {
	found := map[string]bool{}
	if len(wanted) == 0 {
		return found, nil
	}
	cursor := ""
	for {
		groups, next, err := nk.UserGroupsList(ctx, callerID, MaxCrewsPerUser, nil, cursor)
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			state := int(g.GetState().GetValue())
			if state < groupStateSuperadmin || state > groupStateMember {
				continue
			}
			for id := range crewMemberSet(ctx, nk, g.GetGroup().GetId()) {
				if wanted[id] {
					found[id] = true
				}
			}
			if len(found) == len(wanted) {
				return found, nil
			}
		}
		if next == "" || len(groups) == 0 {
			return found, nil
		}
		cursor = next
	}
}

// BeforeRtStatusFollow runs before a status follow on the socket. The client
// follows the members of its crew, to show who is online. The hook keeps the
// ids of the caller and of its crew mates and drops every other id. A follow
// by username is refused.
func BeforeRtStatusFollow(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *rtapi.Envelope) (*rtapi.Envelope, error) {
	follow := in.GetStatusFollow()
	if follow == nil {
		return in, nil
	}
	callerID := builtinCaller(ctx)
	if callerID == "" || len(follow.GetUsernames()) > 0 {
		logger.Info("status follow refused: by username or with no session, caller=%s", callerID)
		return nil, errBuiltinCallRefused
	}
	wanted := map[string]bool{}
	for _, id := range follow.GetUserIds() {
		if id != callerID {
			wanted[id] = true
		}
	}
	mates, err := builtinCrewMates(ctx, nk, callerID, wanted)
	if err != nil {
		logger.Error("status follow: crews of %s read failed: %v", callerID, err)
		return nil, errBuiltinInternal
	}
	kept := make([]string, 0, len(follow.GetUserIds()))
	for _, id := range follow.GetUserIds() {
		if id == callerID || mates[id] {
			kept = append(kept, id)
		}
	}
	if dropped := len(follow.GetUserIds()) - len(kept); dropped > 0 {
		logger.Info("status follow: %d ids dropped, not crew mates of caller=%s", dropped, callerID)
	}
	follow.UserIds = kept
	return in, nil
}

// ---------------------------------------------------------------------------
// The calls that no client makes
// ---------------------------------------------------------------------------

// refuseBuiltinCall returns a before hook that refuses one built-in call.
func refuseBuiltinCall[T any](call string) func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, T) (T, error) {
	return func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in T) (T, error) {
		var none T
		logger.Warn("built-in call refused: call=%s caller=%s", call, builtinCaller(ctx))
		return none, errBuiltinCallRefused
	}
}

// refuseBuiltinRt returns a before hook that refuses one realtime message.
func refuseBuiltinRt(message string) func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, *rtapi.Envelope) (*rtapi.Envelope, error) {
	return func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *rtapi.Envelope) (*rtapi.Envelope, error) {
		logger.Warn("realtime message refused: message=%s caller=%s", message, builtinCaller(ctx))
		return nil, errBuiltinCallRefused
	}
}

// registerBuiltinAPIHooks registers every hook of this file.
func registerBuiltinAPIHooks(initializer runtime.Initializer) error {
	steps := []func() error{
		// The calls that a client makes, each with its rule.
		func() error { return initializer.RegisterBeforeJoinGroup(BeforeJoinGroup) },
		func() error { return initializer.RegisterBeforeListGroupUsers(BeforeListGroupUsers) },
		func() error { return initializer.RegisterBeforeListUserGroups(BeforeListUserGroups) },
		func() error { return initializer.RegisterBeforeListGroups(BeforeListGroups) },
		func() error { return initializer.RegisterBeforeGetUsers(BeforeGetUsers) },
		func() error { return initializer.RegisterBeforeListChannelMessages(BeforeListChannelMessages) },
		func() error { return initializer.RegisterAfterGetUsers(AfterGetUsers) },
		func() error { return initializer.RegisterAfterListGroupUsers(AfterListGroupUsers) },
		func() error { return initializer.RegisterBeforeRt("ChannelJoin", BeforeRtChannelJoin) },
		func() error { return initializer.RegisterBeforeRt("StatusFollow", BeforeRtStatusFollow) },

		// Crew changes that have an RPC with its own rules: update_crew,
		// change_crew_role, join_by_invite_code.
		func() error {
			return initializer.RegisterBeforeUpdateGroup(refuseBuiltinCall[*api.UpdateGroupRequest]("UpdateGroup"))
		},
		func() error {
			return initializer.RegisterBeforeAddGroupUsers(refuseBuiltinCall[*api.AddGroupUsersRequest]("AddGroupUsers"))
		},
		func() error {
			return initializer.RegisterBeforeBanGroupUsers(refuseBuiltinCall[*api.BanGroupUsersRequest]("BanGroupUsers"))
		},
		func() error {
			return initializer.RegisterBeforePromoteGroupUsers(refuseBuiltinCall[*api.PromoteGroupUsersRequest]("PromoteGroupUsers"))
		},
		func() error {
			return initializer.RegisterBeforeDemoteGroupUsers(refuseBuiltinCall[*api.DemoteGroupUsersRequest]("DemoteGroupUsers"))
		},

		// Friends and stored notifications. No client has them.
		func() error {
			return initializer.RegisterBeforeAddFriends(refuseBuiltinCall[*api.AddFriendsRequest]("AddFriends"))
		},
		func() error {
			return initializer.RegisterBeforeBlockFriends(refuseBuiltinCall[*api.BlockFriendsRequest]("BlockFriends"))
		},
		func() error {
			return initializer.RegisterBeforeDeleteFriends(refuseBuiltinCall[*api.DeleteFriendsRequest]("DeleteFriends"))
		},
		func() error {
			return initializer.RegisterBeforeImportFacebookFriends(refuseBuiltinCall[*api.ImportFacebookFriendsRequest]("ImportFacebookFriends"))
		},
		func() error {
			return initializer.RegisterBeforeImportSteamFriends(refuseBuiltinCall[*api.ImportSteamFriendsRequest]("ImportSteamFriends"))
		},
		func() error {
			return initializer.RegisterBeforeListFriends(refuseBuiltinCall[*api.ListFriendsRequest]("ListFriends"))
		},
		func() error {
			return initializer.RegisterBeforeListNotifications(refuseBuiltinCall[*api.ListNotificationsRequest]("ListNotifications"))
		},
		func() error {
			return initializer.RegisterBeforeDeleteNotifications(refuseBuiltinCall[*api.DeleteNotificationsRequest]("DeleteNotifications"))
		},

		// Leaderboards, tournaments, matches and purchases. m3llo has none.
		func() error {
			return initializer.RegisterBeforeListLeaderboardRecords(refuseBuiltinCall[*api.ListLeaderboardRecordsRequest]("ListLeaderboardRecords"))
		},
		func() error {
			return initializer.RegisterBeforeListLeaderboardRecordsAroundOwner(refuseBuiltinCall[*api.ListLeaderboardRecordsAroundOwnerRequest]("ListLeaderboardRecordsAroundOwner"))
		},
		func() error {
			return initializer.RegisterBeforeWriteLeaderboardRecord(refuseBuiltinCall[*api.WriteLeaderboardRecordRequest]("WriteLeaderboardRecord"))
		},
		func() error {
			return initializer.RegisterBeforeDeleteLeaderboardRecord(refuseBuiltinCall[*api.DeleteLeaderboardRecordRequest]("DeleteLeaderboardRecord"))
		},
		func() error {
			return initializer.RegisterBeforeListTournaments(refuseBuiltinCall[*api.ListTournamentsRequest]("ListTournaments"))
		},
		func() error {
			return initializer.RegisterBeforeListTournamentRecords(refuseBuiltinCall[*api.ListTournamentRecordsRequest]("ListTournamentRecords"))
		},
		func() error {
			return initializer.RegisterBeforeListTournamentRecordsAroundOwner(refuseBuiltinCall[*api.ListTournamentRecordsAroundOwnerRequest]("ListTournamentRecordsAroundOwner"))
		},
		func() error {
			return initializer.RegisterBeforeWriteTournamentRecord(refuseBuiltinCall[*api.WriteTournamentRecordRequest]("WriteTournamentRecord"))
		},
		func() error {
			return initializer.RegisterBeforeDeleteTournamentRecord(refuseBuiltinCall[*api.DeleteTournamentRecordRequest]("DeleteTournamentRecord"))
		},
		func() error {
			return initializer.RegisterBeforeJoinTournament(refuseBuiltinCall[*api.JoinTournamentRequest]("JoinTournament"))
		},
		func() error {
			return initializer.RegisterBeforeListMatches(refuseBuiltinCall[*api.ListMatchesRequest]("ListMatches"))
		},
		func() error {
			return initializer.RegisterBeforeListSubscriptions(refuseBuiltinCall[*api.ListSubscriptionsRequest]("ListSubscriptions"))
		},
		func() error {
			return initializer.RegisterBeforeGetSubscription(refuseBuiltinCall[*api.GetSubscriptionRequest]("GetSubscription"))
		},
		func() error {
			return initializer.RegisterBeforeValidatePurchaseApple(refuseBuiltinCall[*api.ValidatePurchaseAppleRequest]("ValidatePurchaseApple"))
		},
		func() error {
			return initializer.RegisterBeforeValidatePurchaseGoogle(refuseBuiltinCall[*api.ValidatePurchaseGoogleRequest]("ValidatePurchaseGoogle"))
		},
		func() error {
			return initializer.RegisterBeforeValidatePurchaseHuawei(refuseBuiltinCall[*api.ValidatePurchaseHuaweiRequest]("ValidatePurchaseHuawei"))
		},
		func() error {
			return initializer.RegisterBeforeValidatePurchaseFacebookInstant(refuseBuiltinCall[*api.ValidatePurchaseFacebookInstantRequest]("ValidatePurchaseFacebookInstant"))
		},
		func() error {
			return initializer.RegisterBeforeValidateSubscriptionApple(refuseBuiltinCall[*api.ValidateSubscriptionAppleRequest]("ValidateSubscriptionApple"))
		},
		func() error {
			return initializer.RegisterBeforeValidateSubscriptionGoogle(refuseBuiltinCall[*api.ValidateSubscriptionGoogleRequest]("ValidateSubscriptionGoogle"))
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}

	// The realtime messages that no client sends. A client sends ChannelJoin,
	// ChannelLeave, the three ChannelMessage calls, StatusFollow, Rpc and
	// Ping. Nakama has no hook before MatchDataSend and PartyDataSend: with
	// no match and no party there is nothing to send them to.
	for _, message := range []string{
		"StatusUnfollow", "StatusUpdate",
		"MatchCreate", "MatchJoin", "MatchLeave", "MatchmakerAdd", "MatchmakerRemove",
		"PartyCreate", "PartyJoin", "PartyLeave", "PartyPromote", "PartyAccept", "PartyRemove", "PartyClose",
		"PartyJoinRequestList", "PartyMatchmakerAdd", "PartyMatchmakerRemove",
	} {
		if err := initializer.RegisterBeforeRt(message, refuseBuiltinRt(message)); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The sweep of join requests
// ---------------------------------------------------------------------------
//
// A join request is a group_edge row with state 3. No m3llo client shows a
// join request or accepts one, and BeforeJoinGroup lets a client make none.
// An invite code joins the user with or without such a row. The sweep
// removes the join requests that exist.

// pendingJoinRequest is one join request row: a crew and the user who asked.
type pendingJoinRequest struct {
	CrewID string
	UserID string
}

// joinRequestStore reads the join request rows of the group_edge table.
type joinRequestStore interface {
	// PendingJoinRequests returns up to limit join requests of groups that
	// exist, in a fixed order.
	PendingJoinRequests(ctx context.Context, limit int) ([]pendingJoinRequest, error)
}

// joinRequests returns the store of this install, or nil when the module has
// no database handle. A var so a test gives a fake.
var joinRequests = func() joinRequestStore {
	if globalDb == nil {
		return nil
	}
	return sqlJoinRequests{db: globalDb}
}

type sqlJoinRequests struct {
	db *sql.DB
}

func (s sqlJoinRequests) PendingJoinRequests(ctx context.Context, limit int) ([]pendingJoinRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ge.source_id, ge.destination_id FROM group_edge ge
		 WHERE ge.state = $1
		   AND EXISTS (SELECT 1 FROM groups g WHERE g.id = ge.source_id)
		 ORDER BY ge.source_id, ge.destination_id LIMIT $2`, groupStateJoinRequest, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingJoinRequest
	for rows.Next() {
		var r pendingJoinRequest
		if err := rows.Scan(&r.CrewID, &r.UserID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const joinRequestSweepBatch = 200

// sweepJoinRequests removes every join request row. It returns the number of
// rows it removed and the number of crews they were in. A kick with no
// caller is how the runtime declines a join request: it removes the row,
// keeps the member count and writes no chat message.
func sweepJoinRequests(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, store joinRequestStore) (rows, crews int) {
	tried := map[pendingJoinRequest]bool{}  // a kick was asked for the row
	stayed := map[pendingJoinRequest]bool{} // the row was there again after its kick
	limit := joinRequestSweepBatch
	for {
		batch, err := store.PendingJoinRequests(ctx, limit)
		if err != nil {
			logger.Error("join request sweep: read failed: %v", err)
			break
		}
		fresh := map[string][]string{}
		for _, r := range batch {
			if tried[r] {
				stayed[r] = true
				continue
			}
			tried[r] = true
			fresh[r.CrewID] = append(fresh[r.CrewID], r.UserID)
		}
		if len(fresh) == 0 {
			if len(batch) < limit {
				// Every row that is left stayed after its kick.
				break
			}
			// The rows that stay fill the batch. Read further.
			limit *= 2
			continue
		}
		for crewID, userIDs := range fresh {
			if err := nk.GroupUsersKick(ctx, "", crewID, userIDs); err != nil {
				logger.Warn("join request sweep: crew %s: %v", crewID, err)
			}
		}
	}
	swept := map[string]bool{}
	for r := range tried {
		if !stayed[r] {
			rows++
			swept[r.CrewID] = true
		}
	}
	return rows, len(swept)
}

// StartJoinRequestSweep runs one pass of the sweep after the module starts.
func StartJoinRequestSweep(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger) {
	store := joinRequests()
	if store == nil {
		logger.Warn("join request sweep: no database handle, not run")
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}
	rows, crews := sweepJoinRequests(ctx, logger, nk, store)
	logger.Info("join request sweep: removed %d join requests in %d crews", rows, crews)
}
