package main

// Device push text by account language (specs/features/LOCALIZATION.md, 9b).
//
// Each push message has a stable key. The English table is complete. The
// other tables are empty until a translator fills them. A message that a table
// does not have falls back to the next table in the chain: the exact tag, then
// the primary subtag, then English.
//
// A message is a fmt pattern. The English patterns use %s and %d in order. A
// translation that must change the order of the values uses explicit indexes,
// for example "%[2]s ... %[1]s". The values of a key keep the order of the
// English pattern.

import (
	"context"
	"fmt"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

// pushTextEnglish is the source table. The keys are stable: a translation
// table uses the same keys.
var pushTextEnglish = map[string]string{
	// Weekly recap (recap_push.go). Values in order: name, belt, held count.
	"recap_took":          "%s took %s. You hold %d.",
	"recap_you_took":      "You took %s. You hold %d.",
	"recap_you_took_from": "You took %s from %s. You hold %d.",
	"recap_week_lead":     "Week %d recap. %s leads with %d.",

	// Belt moved (event_push.go).
	"belt_took_from_you": "%s took %s from you. You hold %d.",
	"belt_took_more":     "%s took %s and %d more. You hold %d.",
	"belt_you_took_from": "You took %s from %s. You hold %d.",
	"belt_you_took_more": "You took %s and %d more. You hold %d.",

	// Friend landed (crew_join_push.go). Values: name, (count), crew name.
	"join_one_say_hi":    "%s joined %s. Say hi.",
	"join_one":           "%s joined %s.",
	"join_one_other":     "%s and 1 other joined %s.",
	"join_others":        "%s and %d others joined %s.",
	"join_name_fallback": "A new member",

	// Mentions (push_remote.go, push_coalesce.go).
	"mentions_new":        "%d new mentions",
	"mentions_new_in":     "%d new mentions in %s",
	"mention_crews_sep":   ", ",
	"push_title_fallback": "Mello",

	// Plain messages (message_push.go, push_coalesce.go). Values: name.
	"messages_new":            "%d new messages",
	"messages_new_in":         "%d new messages in %s",
	"message_sent_gif":        "%s sent a GIF",
	"message_sent_image":      "%s sent an image",
	"message_sender_fallback": "Someone",

	// Belt labels, by the title_id that the client uses (client/src/head2head.rs).
	titleTheCarry:   "The Carry",
	titleLockedIn:   "Locked In",
	titleHasALife:   "Has a Life",
	titleNightOwl:   "Night Owl",
	titleTheCollect: "The Collector",
	titleSampler:    "Sampler",
	titleOneTrick:   "One Trick",
	titleMouth:      "Mouth",
	titleMarathon:   "Marathon",
	titleLastOneUp:  "Last One Up",
	titleClipper:    "Clipper",
	titleRageQuit:   "Rage Quit",
	titleWinner:     "Winner",
}

// pushTexts holds a table per BCP 47 tag. An empty table means "not
// translated yet": each message then shows in English.
var pushTexts = map[string]map[string]string{
	"en":    pushTextEnglish,
	"zh-CN": {},
	"es":    {},
	"pt-BR": {},
	"de":    {},
	"ja":    {},
	"fr":    {},
	"sv":    {},
}

// pushTextChain is the lookup order for a tag: the exact tag, then the primary
// subtag, then English. The match ignores case, and "_" counts as "-".
func pushTextChain(lang string) []map[string]string {
	lang = strings.ReplaceAll(strings.TrimSpace(lang), "_", "-")
	var chain []map[string]string
	add := func(tag string) {
		for k, t := range pushTexts {
			if strings.EqualFold(k, tag) {
				chain = append(chain, t)
				return
			}
		}
	}
	if lang != "" {
		add(lang)
		if i := strings.Index(lang, "-"); i > 0 {
			add(lang[:i])
		}
	}
	return append(chain, pushTextEnglish)
}

// pushText is the message key in the language lang, formatted with args. The
// empty tag is English. An unknown key gives the key itself.
func pushText(lang, key string, args ...any) string {
	for _, t := range pushTextChain(lang) {
		if p, ok := t[key]; ok && p != "" {
			if len(args) == 0 {
				return p
			}
			return fmt.Sprintf(p, args...)
		}
	}
	return key
}

// pushBeltLabel is the label of a belt in lang. An unknown id gives the id.
func pushBeltLabel(lang, titleID string) string {
	return pushText(lang, titleID)
}

// pushUsers reads the users in one batched call. It returns the name shown in
// a push (the display name, else the username, cleaned for the copy rules)
// and the account language (lang_tag) of each user.
func pushUsers(ctx context.Context, nk runtime.NakamaModule, ids []string) (names, langs map[string]string) {
	names = make(map[string]string, len(ids))
	langs = make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names, langs
	}
	users, err := nk.UsersGetId(ctx, ids, nil)
	if err != nil {
		return names, langs
	}
	for _, u := range users {
		name := cleanPushName(u.GetDisplayName())
		if name == "" {
			name = cleanPushName(u.GetUsername())
		}
		names[u.GetId()] = name
		langs[u.GetId()] = u.GetLangTag()
	}
	return names, langs
}
