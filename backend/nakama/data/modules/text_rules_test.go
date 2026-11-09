package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

func TestCleanName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"chinese", "小明的朋友们", "小明的朋友们"},
		{"ascii spaces trimmed", "  Max  ", "Max"},
		{"unicode spaces trimmed", "\u3000小明\u00A0", "小明"},
		{"inner space kept", "José María", "José María"},
		{"right-to-left override removed", "Ev\u202Eil crew", "Evil crew"},
		{"embeddings removed", "\u202Aa\u202Bb\u202Cc\u202Dd", "abcd"},
		{"isolates removed", "a\u2066b\u2067c\u2068d\u2069", "abcd"},
		{"zero width space and BOM removed", "\u200BMax\uFEFF", "Max"},
		{"control characters removed", "Tab\tName\n\x00\x7f\u0085", "TabName"},
		{"zwj family kept", "👨\u200D👩\u200D👧", "👨\u200D👩\u200D👧"},
		{"zwnj kept", "می\u200Cخواهم", "می\u200Cخواهم"},
		{"variation selector kept", "❤\uFE0F crew", "❤\uFE0F crew"},
		{"skin tone kept", "👍🏽 ok", "👍🏽 ok"},
		{"flag kept", "🇸🇪 Nils", "🇸🇪 Nils"},
		{"arabic", "مريم", "مريم"},
		{"space before a removed rune trimmed", " \u200B Max \u202E ", "Max"},
		{"only removed runes", "\u200B\u202E\uFEFF", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := cleanName(c.in)
			if err != nil {
				t.Fatalf("cleanName(%q): %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("cleanName(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCleanNameRejectsInvalidUTF8(t *testing.T) {
	_, err := cleanName("Max\xff")
	if !errors.Is(err, ErrNameInvalidUTF8) {
		t.Fatalf("err = %v, want ErrNameInvalidUTF8", err)
	}
}

func TestNameLengthCountsRunes(t *testing.T) {
	if n := nameLength("小明"); n != 2 {
		t.Errorf("nameLength(小明) = %d, want 2", n)
	}
	if n := nameLength("Jürgen"); n != 6 {
		t.Errorf("nameLength(Jürgen) = %d, want 6", n)
	}
}

// A crew name of 30 characters, made under the old 32-byte limit.
var longCrewName = strings.Repeat("a", 30)

func TestCrewNameFrom(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		stored  string
		want    string
		wantErr bool
	}{
		{"chinese", "小明的朋友们", "", "小明的朋友们", false},
		{"11 cjk characters (33 bytes)", strings.Repeat("明", 11), "", strings.Repeat("明", 11), false},
		{"24 cjk characters", strings.Repeat("明", 24), "", strings.Repeat("明", 24), false},
		{"25 cjk characters", strings.Repeat("明", 25), "", "", true},
		{"24 ascii characters", strings.Repeat("a", 24), "", strings.Repeat("a", 24), false},
		{"25 ascii characters", strings.Repeat("a", 25), "", "", true},
		{"one character", "a", "", "", true},
		{"empty", "", "", "", true},
		{"only white space", " \u3000\t ", "", "", true},
		{"only removed runes", "\u200B\u202E", "", "", true},
		{"override stored without it", "Ev\u202Eil crew", "", "Evil crew", false},
		{"emoji name", "🔥 ranked", "", "🔥 ranked", false},
		{"update keeps a long name", longCrewName, longCrewName, longCrewName, false},
		{"update keeps a long name with white space", " " + longCrewName + " ", longCrewName, longCrewName, false},
		{"update to another long name", longCrewName + "b", longCrewName, "", true},
		{"update to a short name", "New name", longCrewName, "New name", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := crewNameFrom(c.raw, c.stored)
			if c.wantErr {
				if err == nil {
					t.Fatalf("crewNameFrom(%q, %q) = %q, want an error", c.raw, c.stored, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("crewNameFrom(%q, %q): %v", c.raw, c.stored, err)
			}
			if got != c.want {
				t.Errorf("crewNameFrom(%q, %q) = %q, want %q", c.raw, c.stored, got, c.want)
			}
		})
	}
}

func TestCrewNameErrorStatesTheLimit(t *testing.T) {
	_, err := crewNameFrom(strings.Repeat("a", 25), "")
	if err == nil || !strings.Contains(err.Error(), "2-24 characters") {
		t.Fatalf("err = %v, want the 2-24 limit", err)
	}
}

func TestChannelNameFrom(t *testing.T) {
	longChannel := strings.Repeat("c", 30)
	cases := []struct {
		name    string
		raw     string
		stored  string
		want    string
		wantErr bool
	}{
		{"emoji name", "🔥 ranked", "", "🔥 ranked", false},
		{"one character", "a", "", "a", false},
		{"one cjk character", "明", "", "明", false},
		{"24 cjk characters", strings.Repeat("明", 24), "", strings.Repeat("明", 24), false},
		{"25 cjk characters", strings.Repeat("明", 25), "", "", true},
		{"only white space", "   ", "", "", true},
		{"only removed runes", "\uFEFF\u2066", "", "", true},
		{"override stored without it", "lobby\u202E", "", "lobby", false},
		{"rename keeps a long name", longChannel, longChannel, longChannel, false},
		{"rename to another long name", longChannel + "x", longChannel, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := channelNameFrom(c.raw, c.stored)
			if c.wantErr {
				if err == nil {
					t.Fatalf("channelNameFrom(%q, %q) = %q, want an error", c.raw, c.stored, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("channelNameFrom(%q, %q): %v", c.raw, c.stored, err)
			}
			if got != c.want {
				t.Errorf("channelNameFrom(%q, %q) = %q, want %q", c.raw, c.stored, got, c.want)
			}
		})
	}
}

func TestDisplayNameFrom(t *testing.T) {
	long := strings.Repeat("d", 40)
	cases := []struct {
		name    string
		raw     string
		stored  string
		want    string
		wantErr bool
	}{
		{"empty passes", "", "", "", false},
		{"cyrillic", "Олена Шевченко", "", "Олена Шевченко", false},
		{"japanese", "タナカ ユウ", "", "タナカ ユウ", false},
		{"emoji", "🔥Max🔥", "", "🔥Max🔥", false},
		{"cleaned", "  Jürgen\u202E ", "", "Jürgen", false},
		{"32 cjk characters", strings.Repeat("明", 32), "", strings.Repeat("明", 32), false},
		{"33 cjk characters", strings.Repeat("明", 33), "", "", true},
		{"only white space", "  ", "", "", true},
		{"unchanged long name", long, long, long, false},
		{"another long name", long + "x", long, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := displayNameFrom(c.raw, func() string { return c.stored })
			if c.wantErr {
				if err == nil {
					t.Fatalf("displayNameFrom(%q) = %q, want an error", c.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("displayNameFrom(%q): %v", c.raw, err)
			}
			if got != c.want {
				t.Errorf("displayNameFrom(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestChatBodyLimitCountsRunes(t *testing.T) {
	body := func(n int) string {
		raw, err := json.Marshal(map[string]interface{}{
			"v":    1,
			"type": "text",
			"body": strings.Repeat("字", n),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return string(raw)
	}
	if err := validateChatContent(body(2000), ""); err != nil {
		t.Errorf("2000 Chinese characters: %v", err)
	}
	if err := validateChatContent(body(2001), ""); err == nil {
		t.Error("2001 Chinese characters passed")
	}
}

func TestNameMatchesQueryFoldsEveryScript(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  bool
	}{
		{"Jürgen", "JÜRGEN", true},
		{"Олена", "олена", true},
		{"Олена Шевченко", "ШЕВ", true},
		{"José María", "maría", true},
		{"小明", "小明", true},
		{"Jürgen", "jurgen", false},
	}
	for _, c := range cases {
		if got := nameMatchesQuery(c.name, strings.ToLower(c.query)); got != c.want {
			t.Errorf("nameMatchesQuery(%q, %q) = %v, want %v", c.name, c.query, got, c.want)
		}
	}
}

// updateCrewNk adds GroupUpdate to the guest fake and records its name.
type updateCrewNk struct {
	*fakeGuestNk
	updatedName string
	updated     bool
}

func (f *updateCrewNk) GroupUpdate(_ context.Context, _, _, name, _, _, _, _ string, _ bool, _ map[string]interface{}, _ int) error {
	f.updated = true
	f.updatedName = name
	return nil
}

func TestUpdateCrewKeepsALongNameAndChangesAnotherField(t *testing.T) {
	nk := &updateCrewNk{fakeGuestNk: newFakeGuestNk()}
	nk.groups["crew-1"] = &api.Group{Id: "crew-1", Name: longCrewName}
	nk.addMember("crew-1", "owner", "Owner")

	payload := `{"crew_id":"crew-1","name":"` + longCrewName + `","description":"new text"}`
	if _, err := UpdateCrewRPC(ctxWithUser("owner"), noopLogger{}, nil, nk, payload); err != nil {
		t.Fatalf("update with the stored 30-character name: %v", err)
	}
	if !nk.updated || nk.updatedName != longCrewName {
		t.Fatalf("GroupUpdate name = %q (called %v), want the stored name", nk.updatedName, nk.updated)
	}

	nk.updated = false
	payload = `{"crew_id":"crew-1","name":"` + longCrewName + `b"}`
	_, err := UpdateCrewRPC(ctxWithUser("owner"), noopLogger{}, nil, nk, payload)
	var rerr *runtime.Error
	if !errors.As(err, &rerr) || rerr.Code != 3 {
		t.Fatalf("a new 31-character name: err = %v, want code 3", err)
	}
	if nk.updated {
		t.Fatal("GroupUpdate ran for a rejected name")
	}
}
