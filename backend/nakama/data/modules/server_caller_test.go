package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// A made-up value. No stack uses it.
const testServerSecret = "test-server-rpc-secret-made-up"

type rpcHandler func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, string) (string, error)

// serverCallCtx is the context of a call made with the HTTP key: no user id.
// headers are the request headers, as Nakama passes them to an RPC.
func serverCallCtx(headers map[string][]string) context.Context {
	if headers == nil {
		return context.Background()
	}
	return context.WithValue(context.Background(), runtime.RUNTIME_CTX_HEADERS, headers)
}

func serverSecretCtx(value string) context.Context {
	return serverCallCtx(map[string][]string{"X-Mello-Server-Secret": {value}})
}

func setServerSecretT(t *testing.T, value string) {
	t.Helper()
	t.Setenv("MELLO_SERVER_RPC_SECRET", value)
}

// unsetServerSecretT removes the variable for one test. t.Setenv restores the
// value of the process when the test ends.
func unsetServerSecretT(t *testing.T) {
	t.Helper()
	t.Setenv("MELLO_SERVER_RPC_SECRET", "")
	if err := os.Unsetenv("MELLO_SERVER_RPC_SECRET"); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// requireServerCaller
// ---------------------------------------------------------------------------

func TestServerCaller_VariableNotSet_AnswersAsBefore(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*testing.T)
	}{
		{"not set", unsetServerSecretT},
		{"empty", func(t *testing.T) { setServerSecretT(t, "") }},
		{"spaces only", func(t *testing.T) { setServerSecretT(t, "  ") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.set(t)
			if err := requireServerCaller(serverCallCtx(nil), "x"); err != nil {
				t.Fatalf("a call with no session and no header: %v, want no error", err)
			}
			if err := requireServerCaller(serverSecretCtx("any"), "x"); err != nil {
				t.Fatalf("a call with no session and a header: %v, want no error", err)
			}
			if err := requireServerCaller(ctxWithUser("user_1"), "x"); errCode(err) != 7 {
				t.Fatalf("a client session: %v, want PERMISSION_DENIED", err)
			}
		})
	}
}

func TestServerCaller_VariableSet_NeedsTheHeader(t *testing.T) {
	setServerSecretT(t, testServerSecret)
	withUser := func(ctx context.Context) context.Context {
		return context.WithValue(ctx, runtime.RUNTIME_CTX_USER_ID, "user_1")
	}
	for _, tc := range []struct {
		name string
		ctx  context.Context
		pass bool
	}{
		{"no header map", serverCallCtx(nil), false},
		{"no such header", serverCallCtx(map[string][]string{"Content-Type": {"application/json"}}), false},
		{"empty header", serverSecretCtx(""), false},
		{"wrong header", serverSecretCtx("wrong-value"), false},
		{"a prefix of the secret", serverSecretCtx(testServerSecret[:len(testServerSecret)-1]), false},
		{"the secret and one more character", serverSecretCtx(testServerSecret + "x"), false},
		{"the secret in other case", serverSecretCtx(strings.ToUpper(testServerSecret)), false},
		{"the secret in another header", serverCallCtx(map[string][]string{"X-Other": {testServerSecret}}), false},
		{"right header", serverSecretCtx(testServerSecret), true},
		// A gRPC call carries its headers as metadata, with lower case names.
		{"right header, lower case name", serverCallCtx(map[string][]string{"x-mello-server-secret": {testServerSecret}}), true},
		{"right header and a client session", withUser(serverSecretCtx(testServerSecret)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireServerCaller(tc.ctx, "x")
			if tc.pass && err != nil {
				t.Fatalf("%v, want no error", err)
			}
			if !tc.pass && errCode(err) != 7 {
				t.Fatalf("%v, want PERMISSION_DENIED", err)
			}
		})
	}
}

// The value of the variable can end with a line break when it comes from a
// file or a dashboard field. The check must not refuse every caller then.
func TestServerCaller_IgnoresSpaceAroundTheStoredValue(t *testing.T) {
	setServerSecretT(t, " "+testServerSecret+"\n")
	if err := requireServerCaller(serverSecretCtx(testServerSecret), "x"); err != nil {
		t.Fatalf("right header: %v, want no error", err)
	}
	if err := requireServerCaller(serverCallCtx(nil), "x"); errCode(err) != 7 {
		t.Fatalf("no header: %v, want PERMISSION_DENIED", err)
	}
}

// ---------------------------------------------------------------------------
// The dashboard RPCs. Each one serves its cache here, so no database is read.
// ---------------------------------------------------------------------------

const cachedAnswer = `{"cached":true}`

func primeDashboardCaches(t *testing.T) {
	t.Helper()
	dashboardStatsMu.Lock()
	oldStats, oldStatsAt := dashboardStatsJSON, dashboardStatsAt
	dashboardStatsJSON, dashboardStatsAt = cachedAnswer, time.Now()
	dashboardStatsMu.Unlock()

	liveSnapshotMu.Lock()
	oldLive, oldLiveAt := liveSnapshotJSON, liveSnapshotAt
	liveSnapshotJSON, liveSnapshotAt = cachedAnswer, time.Now()
	liveSnapshotMu.Unlock()

	recentSignupsMu.Lock()
	oldSignups, hadSignups := recentSignupsCache[recentSignupsDefaultLimit]
	recentSignupsCache[recentSignupsDefaultLimit] = recentSignupsCacheEntry{json: cachedAnswer, at: time.Now()}
	recentSignupsMu.Unlock()

	t.Cleanup(func() {
		dashboardStatsMu.Lock()
		dashboardStatsJSON, dashboardStatsAt = oldStats, oldStatsAt
		dashboardStatsMu.Unlock()
		liveSnapshotMu.Lock()
		liveSnapshotJSON, liveSnapshotAt = oldLive, oldLiveAt
		liveSnapshotMu.Unlock()
		recentSignupsMu.Lock()
		if hadSignups {
			recentSignupsCache[recentSignupsDefaultLimit] = oldSignups
		} else {
			delete(recentSignupsCache, recentSignupsDefaultLimit)
		}
		recentSignupsMu.Unlock()
	})
}

var dashboardRPCs = []struct {
	id string
	fn rpcHandler
}{
	{"admin_dashboard_stats", AdminDashboardStatsRPC},
	{"admin_live_snapshot", AdminLiveSnapshotRPC},
	{"admin_recent_signups", AdminRecentSignupsRPC},
}

func TestDashboardRPCs_VariableNotSet_AnswerAsBefore(t *testing.T) {
	unsetServerSecretT(t)
	primeDashboardCaches(t)
	for _, rpc := range dashboardRPCs {
		out, err := rpc.fn(serverCallCtx(nil), testLogger(), nil, nil, "")
		if err != nil || out != cachedAnswer {
			t.Errorf("%s with no session: %q, %v, want the answer", rpc.id, out, err)
		}
		if _, err := rpc.fn(ctxWithUser("user_1"), testLogger(), nil, nil, ""); errCode(err) != 7 {
			t.Errorf("%s from a client session: %v, want PERMISSION_DENIED", rpc.id, err)
		}
	}
}

func TestDashboardRPCs_VariableSet_NeedTheHeader(t *testing.T) {
	setServerSecretT(t, testServerSecret)
	primeDashboardCaches(t)
	for _, rpc := range dashboardRPCs {
		if out, err := rpc.fn(serverCallCtx(nil), testLogger(), nil, nil, ""); errCode(err) != 7 || out != "" {
			t.Errorf("%s with no header: %q, %v, want PERMISSION_DENIED and no data", rpc.id, out, err)
		}
		if out, err := rpc.fn(serverSecretCtx("wrong-value"), testLogger(), nil, nil, ""); errCode(err) != 7 || out != "" {
			t.Errorf("%s with a wrong header: %q, %v, want PERMISSION_DENIED and no data", rpc.id, out, err)
		}
		out, err := rpc.fn(serverSecretCtx(testServerSecret), testLogger(), nil, nil, "")
		if err != nil || out != cachedAnswer {
			t.Errorf("%s with the right header: %q, %v, want the answer", rpc.id, out, err)
		}
		ctx := context.WithValue(serverSecretCtx(testServerSecret), runtime.RUNTIME_CTX_USER_ID, "user_1")
		if _, err := rpc.fn(ctx, testLogger(), nil, nil, ""); errCode(err) != 7 {
			t.Errorf("%s from a client session with the header: %v, want PERMISSION_DENIED", rpc.id, err)
		}
	}
}

// ---------------------------------------------------------------------------
// RPCs that write, and the feed of the invite page
// ---------------------------------------------------------------------------

func TestServerSecret_AWriteIsRefusedBeforeItChangesAnything(t *testing.T) {
	setServerSecretT(t, testServerSecret)

	nk := inviteStepCrew(t)
	payload := `{"code":"ABCD-EFGH","step":"opened"}`
	if _, err := ReportInviteStepRPC(serverCallCtx(nil), testLogger(), nil, nk, payload); errCode(err) != 7 {
		t.Fatalf("report_invite_step with no header: %v, want PERMISSION_DENIED", err)
	}
	if q := readQuestsT(t, nk, "gc"); q.InviteOpenedMs != 0 {
		t.Fatalf("invite_opened_ms = %d after a refused call", q.InviteOpenedMs)
	}
	if _, err := ReportInviteStepRPC(serverSecretCtx(testServerSecret), testLogger(), nil, nk, payload); err != nil {
		t.Fatalf("report_invite_step with the right header: %v", err)
	}
	if q := readQuestsT(t, nk, "gc"); q.InviteOpenedMs == 0 {
		t.Fatal("invite_opened_ms is not set after an accepted call")
	}

	policy := newFakePolicyNk()
	set := `{"hook_enabled":true,"hook_allow_ids":[1],"hook_deny_ids":[],"version":"","actor":"tester"}`
	if _, err := AdminCapturePolicySetRPC(serverCallCtx(nil), testLogger(), nil, policy, set); errCode(err) != 7 {
		t.Fatalf("admin_capture_policy_set with no header: %v, want PERMISSION_DENIED", err)
	}
	if read := policyGetWith(t, serverSecretCtx(testServerSecret), policy); read.Stored {
		t.Fatalf("a refused write stored a policy: %+v", read)
	}
	if _, err := AdminCapturePolicySetRPC(serverSecretCtx(testServerSecret), testLogger(), nil, policy, set); err != nil {
		t.Fatalf("admin_capture_policy_set with the right header: %v", err)
	}
	if read := policyGetWith(t, serverSecretCtx(testServerSecret), policy); !read.Stored || !read.Policy.HookEnabled {
		t.Fatalf("the accepted write is not stored: %+v", read)
	}
}

func policyGetWith(t *testing.T, ctx context.Context, nk runtime.NakamaModule) AdminCapturePolicyRead {
	t.Helper()
	out, err := AdminCapturePolicyGetRPC(ctx, testLogger(), nil, nk, "{}")
	if err != nil {
		t.Fatalf("admin_capture_policy_get: %v", err)
	}
	var read AdminCapturePolicyRead
	if err := json.Unmarshal([]byte(out), &read); err != nil {
		t.Fatalf("decode admin_capture_policy_get: %v", err)
	}
	return read
}

func TestGuestCrewFeed_IsForServersOnly(t *testing.T) {
	resetVoiceState()
	payload := `{"code":"abcd-efgh"}`

	unsetServerSecretT(t)
	if _, err := GuestCrewFeedRPC(serverCallCtx(nil), testLogger(), nil, seedGuestCrew(), payload); err != nil {
		t.Fatalf("variable not set, no session: %v, want the feed", err)
	}
	if _, err := GuestCrewFeedRPC(ctxWithUser(testAliceID), testLogger(), nil, seedGuestCrew(), payload); errCode(err) != 7 {
		t.Fatalf("variable not set, a client session: %v, want PERMISSION_DENIED", err)
	}

	setServerSecretT(t, testServerSecret)
	if out, err := GuestCrewFeedRPC(serverCallCtx(nil), testLogger(), nil, seedGuestCrew(), payload); errCode(err) != 7 || out != "" {
		t.Fatalf("variable set, no header: %q, %v, want PERMISSION_DENIED and no data", out, err)
	}
	if out, err := GuestCrewFeedRPC(serverSecretCtx("wrong-value"), testLogger(), nil, seedGuestCrew(), payload); errCode(err) != 7 || out != "" {
		t.Fatalf("variable set, wrong header: %q, %v, want PERMISSION_DENIED and no data", out, err)
	}
	out, err := GuestCrewFeedRPC(serverSecretCtx(testServerSecret), testLogger(), nil, seedGuestCrew(), payload)
	if err != nil || !strings.Contains(out, "Night Owls") {
		t.Fatalf("variable set, right header: %q, %v, want the feed", out, err)
	}
}

// ---------------------------------------------------------------------------
// The RPCs that a client calls before it has a session
// ---------------------------------------------------------------------------

// A client build holds the HTTP key and not the server secret. These three
// RPCs answer a call with the HTTP key alone, with the variable set or not.
func TestClientPreSessionRPCs_NeverNeedTheHeader(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*testing.T)
	}{
		{"variable not set", unsetServerSecretT},
		{"variable set", func(t *testing.T) { setServerSecretT(t, testServerSecret) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.set(t)
			ctx := serverCallCtx(nil)

			discoverNk := newFakeDiscoverNk()
			open := discoverNk.addCrew(1, true)
			out, err := DiscoverCrewsRPC(ctx, testLogger(), nil, discoverNk, "{}")
			if err != nil || !strings.Contains(out, open) {
				t.Errorf("discover_crews: %q, %v, want the open crew", out, err)
			}

			avatarNk := newFakeGuestNk()
			avatarNk.put(CrewAvatarCollection, "crew-1", SystemUserID, map[string]string{"data": "aGVsbG8="})
			out, err = GetCrewAvatarRPC(ctx, testLogger(), nil, avatarNk, `{"crew_id":"crew-1"}`)
			if err != nil || !strings.Contains(out, "aGVsbG8=") {
				t.Errorf("get_crew_avatar: %q, %v, want the avatar", out, err)
			}

			inviteNk := giftsCrew(t)
			inviteNk.groups["gc"] = &api.Group{Id: "gc", Name: "pre session crew"}
			code, err := GenerateInviteCode(context.Background(), inviteNk, testLogger(), "gc", "own")
			if err != nil {
				t.Fatal(err)
			}
			out, err = ResolveCrewInviteRPC(ctx, testLogger(), nil, inviteNk, `{"code":"`+code+`"}`)
			if err != nil || !strings.Contains(out, "pre session crew") {
				t.Errorf("resolve_crew_invite: %q, %v, want the crew", out, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Which RPCs have the check
// ---------------------------------------------------------------------------

// serverOnlyRPCs are the RPCs for our own servers. Each one starts with
// requireServerCaller. Add an RPC here when it gets the check.
var serverOnlyRPCs = []string{
	"admin_capture_policy_get",
	"admin_capture_policy_set",
	"admin_dashboard_stats",
	"admin_discover_curation_get",
	"admin_discover_curation_set",
	"admin_feed_config_get",
	"admin_feed_config_set",
	"admin_live_snapshot",
	"admin_media_orphans",
	"admin_media_report_dismiss",
	"admin_media_reports_list",
	"admin_media_takedown",
	"admin_purge_crew",
	"admin_recent_signups",
	"admin_scrub_user",
	"guest_crew_feed",
	"report_invite_step",
}

// clientPreSessionRPCs are called by released clients with the HTTP key, before
// a session exists. A released client cannot send the server secret, so none
// of these may ever get requireServerCaller.
var clientPreSessionRPCs = []string{
	"discover_crews",
	"get_crew_avatar",
	"resolve_crew_invite",
}

// TestServerOnlyRPCs_AreExactlyTheListedOnes reads the module source. It finds
// each registered RPC and whether its handler starts with requireServerCaller.
func TestServerOnlyRPCs_AreExactlyTheListedOnes(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ok := pkgs["main"]
	if !ok {
		t.Fatal("package main not found in the module directory")
	}

	handlerOf := map[string]string{} // RPC id -> handler name
	checkedAs := map[string]string{} // handler name -> the RPC name it passes to the check
	for _, file := range pkg.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				sel, ok := node.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "RegisterRpc" || len(node.Args) != 2 {
					return true
				}
				lit, ok := node.Args[0].(*ast.BasicLit)
				fn, ok2 := node.Args[1].(*ast.Ident)
				if !ok || !ok2 || lit.Kind != token.STRING {
					return true
				}
				id, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("RegisterRpc with an id that is no string: %s", lit.Value)
				}
				handlerOf[id] = fn.Name
			case *ast.FuncDecl:
				if name, found := firstStatementServerCheck(node); found {
					checkedAs[node.Name.Name] = name
				}
			}
			return true
		})
	}
	if len(handlerOf) < 50 {
		t.Fatalf("found %d registered RPCs: the source reader is broken", len(handlerOf))
	}

	var gated []string
	for id, handler := range handlerOf {
		name, found := checkedAs[handler]
		if !found {
			continue
		}
		gated = append(gated, id)
		if name != id {
			t.Errorf("%s passes the name %q to requireServerCaller, want its own id", id, name)
		}
	}
	sort.Strings(gated)
	want := append([]string(nil), serverOnlyRPCs...)
	sort.Strings(want)
	if strings.Join(gated, ",") != strings.Join(want, ",") {
		t.Errorf("RPCs that start with requireServerCaller:\n got  %v\n want %v", gated, want)
	}

	for _, id := range clientPreSessionRPCs {
		handler, registered := handlerOf[id]
		if !registered {
			t.Errorf("%s is not registered: released clients call it", id)
			continue
		}
		if _, found := checkedAs[handler]; found {
			t.Errorf("%s has requireServerCaller: released clients call it with the HTTP key alone", id)
		}
	}
}

// firstStatementServerCheck reports whether the first statement of fn is
// `if err := requireServerCaller(ctx, "<name>"); err != nil`, and the name.
func firstStatementServerCheck(fn *ast.FuncDecl) (string, bool) {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return "", false
	}
	ifStmt, ok := fn.Body.List[0].(*ast.IfStmt)
	if !ok {
		return "", false
	}
	assign, ok := ifStmt.Init.(*ast.AssignStmt)
	if !ok || len(assign.Rhs) != 1 {
		return "", false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok {
		return "", false
	}
	callee, ok := call.Fun.(*ast.Ident)
	if !ok || callee.Name != "requireServerCaller" || len(call.Args) != 2 {
		return "", false
	}
	lit, ok := call.Args[1].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	name, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return name, true
}
