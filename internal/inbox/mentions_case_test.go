package inbox

import "testing"

// GitLab usernames are case-insensitive: people type @Mael.Valais for
// mael.valais.
func TestMentionsIgnoresCase(t *testing.T) {
	tests := []struct {
		body, user string
		want       bool
	}{
		{"ping @Me", "me", true},
		{"ping @ME.", "me", true},
		{"ping @me", "Me", true},
		{"@Mael.Valais can you look?", "mael.valais", true},
		{"thanks @MAEL.VALAIS.", "mael.valais", true},
		{"cc @mael.valais", "Mael.Valais", true},
		{"@Meg", "me", false},
		{"@MEG and @Me.Valais", "me", false},
		{"@Mael.Valais2", "mael.valais", false},
		{"email Me@Me.com", "me", false},
		{"no mention of Me here", "me", false},
		// Only ASCII letters are folded: "İ" is not "i", and the Kelvin
		// sign is not "k" (it ends the username, as any non-ASCII letter).
		{"@MİKE", "mike", false},
		{"@Kevin", "kevin", false},
		{"@maelK", "mael", true},
		{"@MaelK", "mael", true},
		{"@me @me @Me.", "me", true},
		{"@", "me", false},
		{"", "me", false},
	}
	for _, tt := range tests {
		if got := mentions(tt.body, tt.user); got != tt.want {
			t.Errorf("mentions(%q, %q) = %v, want %v", tt.body, tt.user, got, tt.want)
		}
	}
}
