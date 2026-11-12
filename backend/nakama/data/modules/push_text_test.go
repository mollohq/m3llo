package main

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// pushTextSamples gives long sample values for each message key, in the order
// of the English pattern. A name or a crew name is 32 runes, a count is 999.
func pushTextSamples() map[string][]any {
	name := strings.Repeat("Å", 32)
	crew := strings.Repeat("Ö", 32)
	belt := "The Collector" // the longest English label
	return map[string][]any{
		"recap_took":          {name, belt, 999},
		"recap_you_took":      {belt, 999},
		"recap_you_took_from": {belt, name, 999},
		"recap_week_lead":     {53, name, 999},
		"belt_took_from_you":  {name, belt, 999},
		"belt_took_more":      {name, belt, 999, 999},
		"belt_you_took_from":  {belt, name, 999},
		"belt_you_took_more":  {belt, 999, 999},
		"join_one_say_hi":     {name, crew},
		"join_one":            {name, crew},
		"join_one_other":      {name, crew},
		"join_others":         {name, 999, crew},
		"mentions_new":        {999},
		"mentions_new_in":     {999, crew + ", " + crew},
	}
}

// pushTitleKeys are the messages that are a push title, and the belt labels,
// which go inside a body. Both must fit the title limit.
func pushTitleKey(key string) bool {
	if key == "push_title_fallback" {
		return true
	}
	_, belt := head2headTitleLabels[key]
	return belt
}

func checkPushTable(t *testing.T, lang string, table map[string]string) {
	t.Helper()
	samples := pushTextSamples()
	for key, pattern := range table {
		if pattern == "" {
			continue
		}
		args, body := samples[key]
		got := pattern
		if body {
			got = pushText(lang, key, args...)
		}
		if strings.Contains(got, "%!") {
			t.Errorf("%s/%s: the pattern does not take the values: %q", lang, key, got)
		}
		limit := pushBodyMaxRunes
		if pushTitleKey(key) {
			limit = pushTitleMaxRunes
		}
		if n := utf8.RuneCountInString(got); n > limit {
			t.Errorf("%s/%s: %d runes with long values, limit %d: %q", lang, key, n, limit, got)
		}
	}
}

func TestEnglishPushTextFitsTheLimitsWithLongValues(t *testing.T) {
	checkPushTable(t, "en", pushTextEnglish)
	for key := range pushTextEnglish {
		if _, ok := pushTextSamples()[key]; !ok && !pushTitleKey(key) && key != "join_name_fallback" && key != "mention_crews_sep" {
			t.Errorf("%s: no sample values in the test", key)
		}
	}
}

func TestEachPushTranslationHasAllTheKeys(t *testing.T) {
	for lang, table := range pushTexts {
		if lang == "en" || len(table) == 0 {
			continue
		}
		for key := range pushTextEnglish {
			if table[key] == "" {
				t.Errorf("%s: no message %q", lang, key)
			}
		}
		for key := range table {
			if _, ok := pushTextEnglish[key]; !ok {
				t.Errorf("%s: key %q is not in the English table", lang, key)
			}
		}
		checkPushTable(t, lang, table)
	}
}

func TestEveryBeltLabelIsInThePushTable(t *testing.T) {
	for id, label := range head2headTitleLabels {
		if got := pushBeltLabel("", id); got != label {
			t.Errorf("%s: push label %q, recap label %q", id, got, label)
		}
	}
}

// withPushTable puts a test-only table for lang, and restores the old one.
func withPushTable(t *testing.T, lang string, table map[string]string) {
	t.Helper()
	old, had := pushTexts[lang]
	pushTexts[lang] = table
	t.Cleanup(func() {
		if had {
			pushTexts[lang] = old
		} else {
			delete(pushTexts, lang)
		}
	})
}

func TestPushTextFallsBackToTheBaseLanguageThenEnglish(t *testing.T) {
	if got := pushText("", "join_one", "Jo", "Owls"); got != "Jo joined Owls." {
		t.Fatalf("the empty tag is English: %q", got)
	}
	if got := pushText("sv", "join_one", "Jo", "Owls"); got != "Jo joined Owls." {
		t.Fatalf("an empty sv table gives English: %q", got)
	}
	withPushTable(t, "pt", map[string]string{"join_one": "%s entrou em %s."})
	if got := pushText("pt-BR", "join_one", "Jo", "Owls"); got != "Jo entrou em Owls." {
		t.Fatalf("pt-BR with an empty table falls back to pt: %q", got)
	}
	if got := pushText("pt_br", "join_one", "Jo", "Owls"); got != "Jo entrou em Owls." {
		t.Fatalf("the match ignores case and the underscore: %q", got)
	}
	withPushTable(t, "zh", map[string]string{"join_one": "zh %s %s"})
	withPushTable(t, "zh-CN", map[string]string{"join_one": "zh-CN %s %s"})
	if got := pushText("zh-CN", "join_one", "a", "b"); got != "zh-CN a b" {
		t.Fatalf("the exact tag comes before the base: %q", got)
	}
	if got := pushText("zh-TW", "join_one", "a", "b"); got != "zh a b" {
		t.Fatalf("an unknown region falls back to the base: %q", got)
	}
	withPushTable(t, "de", map[string]string{"join_one": "%[2]s: %[1]s ist da."})
	if got := pushText("de", "join_one", "Jo", "Owls"); got != "Owls: Jo ist da." {
		t.Fatalf("a translation can change the order: %q", got)
	}
}

func TestCrewJoinPushUsesTheLanguageOfEachRecipient(t *testing.T) {
	withPushTable(t, "sv", map[string]string{})
	r := newJoinRig(t)
	r.join("u1", "Ann")
	r.nk.users["u1"].LangTag = "sv"
	r.join("u2", "Bo")
	if len(r.sent) != 1 || !strings.HasSuffix(r.sent[0], "|Bo joined Night Owls. Say hi.") {
		t.Fatalf("an empty sv table gives English: %v", r.sent)
	}

	pushTexts["sv"] = map[string]string{"join_one_say_hi": "%s gick med i %s. Säg hej."}
	r.sent = nil
	r.nk.users["u2"].LangTag = "en"
	r.join("u3", "Cy")
	got := strings.Join(r.sent, "\n")
	if !strings.Contains(got, "u1|crew_join|") || !strings.Contains(got, "|Cy gick med i Night Owls. Säg hej.") {
		t.Fatalf("u1 reads Swedish: %v", r.sent)
	}
	if !strings.Contains(got, "|Cy joined Night Owls. Say hi.") {
		t.Fatalf("u2 reads English: %v", r.sent)
	}
}

func TestCrewJoinPushNamesAnUnnamedJoinerInTheRecipientsLanguage(t *testing.T) {
	withPushTable(t, "sv", map[string]string{"join_name_fallback": "En ny medlem"})
	r := newJoinRig(t)
	r.join("u1", "Ann")
	r.nk.users["u1"].LangTag = "sv"
	r.nk.addMember("crew", "u2", "")
	queueCrewJoinPush(context.Background(), testLogger(), r.nk, "crew", "u2")
	if len(r.sent) != 1 || !strings.HasSuffix(r.sent[0], "|En ny medlem joined Night Owls. Say hi.") {
		t.Fatalf("sent: %v", r.sent)
	}
}

func TestBeltPushUsesTheLabelInTheRecipientsLanguage(t *testing.T) {
	withPushTable(t, "sv", map[string]string{
		titleTheCarry:        "Bäraren",
		"belt_took_from_you": "%s tog %s från dig. Du har %d.",
	})
	r := newBeltRig(t)
	r.nk.users["u1"].LangTag = "sv-SE"
	r.move("u2", "u2", "u1", titleTheCarry)
	if len(r.sent) != 1 || !strings.HasSuffix(r.sent[0], "|Bo tog Bäraren från dig. Du har 0.") {
		t.Fatalf("sent: %v", r.sent)
	}
}

func TestRecapPushBodyUsesTheLanguage(t *testing.T) {
	withPushTable(t, "sv", map[string]string{titleTheCarry: "Bäraren", "recap_took": "%s tog %s. Du har %d."})
	if got := recapPushBody("sv", pushRecap(), "bob"); got != "Jonas tog Bäraren. Du har 4." {
		t.Fatalf("sv: %q", got)
	}
	items := buildRecapPushItems(pushRecap(), []string{"bob"}, nil, map[string]string{"bob": "sv"})
	if len(items) != 1 || items[0].Body != "Jonas tog Bäraren. Du har 4." {
		t.Fatalf("items: %+v", items)
	}
	if got := recapPushBody("fr", pushRecap(), "bob"); got != "Jonas took The Carry. You hold 4." {
		t.Fatalf("an empty fr table gives English: %q", got)
	}
}

func TestCoalescedMentionsUseTheRecipientsLanguage(t *testing.T) {
	withPushTable(t, "sv", map[string]string{
		"mentions_new_in":     "%d nya omnämnanden i %s",
		"push_title_fallback": "Mello SV",
	})
	held := map[string]*heldMention{
		"c1": {alert: pushAlert{CrewID: "c1", Title: "Retro", Lang: "sv"}, count: 2, seq: 1},
		"c2": {alert: pushAlert{CrewID: "c2", Title: "Devz", Lang: "sv"}, count: 1, seq: 2},
	}
	got, ok := coalesceHeld(held)
	if !ok || got.Title != "Mello SV" || got.Body != "3 nya omnämnanden i Devz, Retro" {
		t.Fatalf("got %+v", got)
	}
	delete(held, "c2")
	got, _ = coalesceHeld(held)
	if got.Body != "2 new mentions" || got.Title != "Retro" {
		t.Fatalf("a missing message is English: %+v", got)
	}
}

func TestMentionPushTitleFallsBackInTheLanguage(t *testing.T) {
	withPushTable(t, "sv", map[string]string{"push_title_fallback": "Mello SV"})
	if got := mentionPushTitle("sv", ""); got != "Mello SV" {
		t.Fatalf("got %q", got)
	}
	if got := mentionPushTitle("", ""); got != "Mello" {
		t.Fatalf("got %q", got)
	}
	if got := mentionPushTitle("sv", "Retro"); got != "Retro" {
		t.Fatalf("the crew name stays: %q", got)
	}
}
