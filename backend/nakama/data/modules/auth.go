package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// ---------------------------------------------------------------------------
// Provider user types (for custom auth validation)
// ---------------------------------------------------------------------------

type DiscordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Avatar   string `json:"avatar"`
}

type TwitchUser struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

type SteamUser struct {
	ID          string
	PersonaName string
}

// ---------------------------------------------------------------------------
// BeforeAuthenticateCustom — dispatches on provider var to validate tokens
// ---------------------------------------------------------------------------

func BeforeAuthenticateCustom(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AuthenticateCustomRequest) (*api.AuthenticateCustomRequest, error) {
	if in.Account.Vars == nil {
		return in, nil
	}

	provider := in.Account.Vars["provider"]
	token := in.Account.Id // token is passed as the custom ID

	switch provider {
	case "discord":
		return handleDiscordAuth(logger, in, token)
	case "twitch":
		return handleTwitchAuth(logger, in, token)
	case "steam":
		return handleSteamAuth(logger, in, token)
	default:
		return in, nil
	}
}

func handleDiscordAuth(logger runtime.Logger, in *api.AuthenticateCustomRequest, token string) (*api.AuthenticateCustomRequest, error) {
	user, err := validateDiscordToken(token)
	if err != nil {
		logger.Error("Discord validation failed: %v", err)
		return nil, runtime.NewError("Invalid Discord token", 16)
	}

	in.Account.Id = fmt.Sprintf("discord_%s", user.ID)
	if in.Username == "" {
		in.Username = user.Username
	}

	logger.Info("Discord auth for user: %s (%s)", user.Username, user.ID)
	return in, nil
}

func handleTwitchAuth(logger runtime.Logger, in *api.AuthenticateCustomRequest, token string) (*api.AuthenticateCustomRequest, error) {
	user, err := validateTwitchToken(token)
	if err != nil {
		logger.Error("Twitch validation failed: %v", err)
		return nil, runtime.NewError("Invalid Twitch token", 16)
	}

	in.Account.Id = fmt.Sprintf("twitch_%s", user.ID)
	if in.Username == "" {
		in.Username = user.Login
	}

	logger.Info("Twitch auth for user: %s (%s)", user.DisplayName, user.ID)
	return in, nil
}

func handleSteamAuth(logger runtime.Logger, in *api.AuthenticateCustomRequest, token string) (*api.AuthenticateCustomRequest, error) {
	user, err := validateSteamOpenID(token)
	if err != nil {
		logger.Error("Steam validation failed: %v", err)
		return nil, runtime.NewError("Invalid Steam sign-in", 16)
	}

	in.Account.Id = fmt.Sprintf("steam_%s", user.ID)
	if in.Username == "" && user.PersonaName != "" {
		in.Username = user.PersonaName
	}

	logger.Info("Steam auth for user: %s (%s)", user.PersonaName, user.ID)
	return in, nil
}

// ---------------------------------------------------------------------------
// BeforeLinkCustom — same validation as BeforeAuthenticateCustom so that
// linking a Discord/Twitch/Steam identity during onboarding works correctly.
// ---------------------------------------------------------------------------

func BeforeLinkCustom(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AccountCustom) (*api.AccountCustom, error) {
	if in.Vars == nil {
		return in, nil
	}

	provider := in.Vars["provider"]
	token := in.Id

	switch provider {
	case "discord":
		user, err := validateDiscordToken(token)
		if err != nil {
			logger.Error("Discord validation failed (link): %v", err)
			return nil, runtime.NewError("Invalid Discord token", 16)
		}
		in.Id = fmt.Sprintf("discord_%s", user.ID)
		logger.Info("Discord link for user: %s (%s)", user.Username, user.ID)
	case "twitch":
		user, err := validateTwitchToken(token)
		if err != nil {
			logger.Error("Twitch validation failed (link): %v", err)
			return nil, runtime.NewError("Invalid Twitch token", 16)
		}
		in.Id = fmt.Sprintf("twitch_%s", user.ID)
		logger.Info("Twitch link for user: %s (%s)", user.DisplayName, user.ID)
	case "steam":
		user, err := validateSteamOpenID(token)
		if err != nil {
			logger.Error("Steam validation failed (link): %v", err)
			return nil, runtime.NewError("Invalid Steam sign-in", 16)
		}
		in.Id = fmt.Sprintf("steam_%s", user.ID)
		logger.Info("Steam link for user: %s (%s)", user.PersonaName, user.ID)
	}

	return in, nil
}

// ---------------------------------------------------------------------------
// Token validation helpers
// ---------------------------------------------------------------------------

func validateDiscordToken(token string) (*DiscordUser, error) {
	req, err := http.NewRequest("GET", "https://discord.com/api/users/@me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("discord API error: %s", body)
	}

	var user DiscordUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

func validateTwitchToken(token string) (*TwitchUser, error) {
	req, err := http.NewRequest("GET", "https://api.twitch.tv/helix/users", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Client-Id", os.Getenv("TWITCH_CLIENT_ID"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("twitch API error: %s", body)
	}

	var result struct {
		Data []TwitchUser `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no user data from Twitch")
	}
	return &result.Data[0], nil
}

// validateSteamOpenID verifies a Steam OpenID 2.0 response. `token` is the raw
// query string of the `return_to` redirect (the `openid.*` params). We echo the
// params back to Steam with `mode=check_authentication`; Steam answers
// `is_valid:true` only if it issued them. The steamid is parsed from claimed_id
// (`https://steamcommunity.com/openid/id/<steamid>`). Persona name is resolved
// best-effort via the Steam Web API when STEAM_WEB_API_KEY is set.
func validateSteamOpenID(token string) (*SteamUser, error) {
	values, err := url.ParseQuery(token)
	if err != nil {
		return nil, fmt.Errorf("parse openid params: %w", err)
	}
	claimed := values.Get("openid.claimed_id")
	if values.Get("openid.mode") == "" || claimed == "" || values.Get("openid.sig") == "" {
		return nil, fmt.Errorf("incomplete openid response")
	}

	values.Set("openid.mode", "check_authentication")
	resp, err := http.PostForm("https://steamcommunity.com/openid/login", values)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "is_valid:true") {
		return nil, fmt.Errorf("steam openid verification rejected")
	}

	idx := strings.LastIndex(claimed, "/")
	if idx == -1 || idx+1 >= len(claimed) {
		return nil, fmt.Errorf("malformed claimed_id: %s", claimed)
	}
	steamID := claimed[idx+1:]

	return &SteamUser{ID: steamID, PersonaName: resolveSteamPersona(steamID)}, nil
}

// resolveSteamPersona fetches a Steam display name via the Web API. Best-effort:
// returns "" if no key is configured or the call fails (auth still succeeds).
func resolveSteamPersona(steamID string) string {
	key := os.Getenv("STEAM_WEB_API_KEY")
	if key == "" {
		return ""
	}
	u := fmt.Sprintf(
		"https://api.steampowered.com/ISteamUser/GetPlayerSummaries/v2/?key=%s&steamids=%s",
		url.QueryEscape(key), url.QueryEscape(steamID))
	resp, err := http.Get(u)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return ""
	}
	var result struct {
		Response struct {
			Players []struct {
				PersonaName string `json:"personaname"`
			} `json:"players"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ""
	}
	if len(result.Response.Players) == 0 {
		return ""
	}
	return result.Response.Players[0].PersonaName
}

// ---------------------------------------------------------------------------
// AuthProvidersRPC — returns list of enabled auth providers
// ---------------------------------------------------------------------------

func AuthProvidersRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	providers := []string{"email"}

	if os.Getenv("STEAM_PUBLISHER_KEY") != "" {
		providers = append(providers, "steam")
	}
	if os.Getenv("GOOGLE_CLIENT_ID") != "" {
		providers = append(providers, "google")
	}
	if os.Getenv("TWITCH_CLIENT_ID") != "" {
		providers = append(providers, "twitch")
	}
	if os.Getenv("DISCORD_CLIENT_ID") != "" {
		providers = append(providers, "discord")
	}
	if os.Getenv("APPLE_CLIENT_ID") != "" {
		providers = append(providers, "apple")
	}

	resp, _ := json.Marshal(map[string]interface{}{
		"providers": providers,
	})
	return string(resp), nil
}

func AfterAuthenticateEmail(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.Session, in *api.AuthenticateEmailRequest) error {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return nil
	}

	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		logger.Error("failed to get account: %v", err)
		return nil
	}

	user := account.GetUser()
	metadata := user.GetMetadata()
	if metadata != "" && metadata != "{}" {
		return nil
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	tag := fmt.Sprintf("#%04d", rng.Intn(10000))

	displayName := user.GetDisplayName()
	if displayName == "" && in.GetAccount() != nil {
		email := in.GetAccount().GetEmail()
		for i, c := range email {
			if c == '@' {
				displayName = email[:i]
				break
			}
		}
	}

	meta := map[string]interface{}{
		"tag":        tag,
		"created_at": time.Now().Unix(),
	}

	if err := nk.AccountUpdateId(ctx, userID, "", meta, displayName, "", "", "", ""); err != nil {
		logger.Error("failed to initialize user metadata: %v", err)
		return nil
	}

	logger.Info("Initialized new user %s with tag %s", userID, tag)
	return nil
}

// ---------------------------------------------------------------------------
// Display name rule (text_rules.go)
// ---------------------------------------------------------------------------

// BeforeUpdateAccount cleans the display name of an account update with
// cleanName and limits it to DisplayNameMaxChars characters. A request
// without a display name passes unchanged.
func BeforeUpdateAccount(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.UpdateAccountRequest) (*api.UpdateAccountRequest, error) {
	if in.GetDisplayName() == nil {
		return in, nil
	}
	stored := func() string {
		userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
		if !ok {
			return ""
		}
		account, err := nk.AccountGetId(ctx, userID)
		if err != nil {
			logger.Warn("update account: failed to read account %s: %v", userID, err)
			return ""
		}
		return account.GetUser().GetDisplayName()
	}
	name, err := displayNameFrom(in.GetDisplayName().GetValue(), stored)
	if err != nil {
		return nil, err
	}
	in.DisplayName = wrapperspb.String(name)
	return in, nil
}

// displayNameFrom returns the display name to store, from the name in a
// request. An empty name passes unchanged, as before this rule. Otherwise the
// cleaned name must have 1 to DisplayNameMaxChars characters. A longer name
// passes when it is equal to the stored name: the client sends the present
// name with every profile save, and names from before this rule can be
// longer. stored is called only for a name that is too long.
func displayNameFrom(raw string, stored func() string) (string, error) {
	if raw == "" {
		return "", nil
	}
	name, err := cleanName(raw)
	if err != nil {
		return "", runtime.NewError(err.Error(), 3)
	}
	n := nameLength(name)
	if n > DisplayNameMaxChars {
		if s := stored(); s != "" && (raw == s || name == s) {
			return s, nil
		}
	}
	if n < 1 || n > DisplayNameMaxChars {
		return "", runtime.NewError(fmt.Sprintf("display name must be 1-%d characters", DisplayNameMaxChars), 3)
	}
	return name, nil
}
