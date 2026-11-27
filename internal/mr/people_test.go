package mr

import (
	"testing"

	"github.com/maelvls/glcli/internal/gitlab"
)

func TestPeopleHuman(t *testing.T) {
	const sa = "service_account_group_12_ab12cd"
	set := func(names ...string) map[string]bool {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = true
		}
		return m
	}
	tests := []struct {
		name   string
		people People
		user   string
		want   bool
	}{
		{"someone else", People{Me: "me"}, "alice", true},
		{"me", People{Me: "me"}, "me", false},
		{"a bot", People{Me: "me"}, sa, false},
		{"ignored", People{Me: "me", Ignore: set("alice")}, "alice", false},
		{"an included bot", People{Me: "me", Include: set(sa)}, sa, true},
		{"another bot than the included one", People{Me: "me", Include: set(sa)}, "project_1_bot_abc", false},
		{"included and ignored: ignored wins", People{Me: "me", Ignore: set(sa), Include: set(sa)}, sa, false},
		{"me, even when included", People{Me: "me", Include: set("me")}, "me", false},
		{"me as a bot, even when included", People{Me: sa, Include: set(sa)}, sa, false},
		{"including someone who is not a bot changes nothing", People{Me: "me", Include: set("alice")}, "alice", true},
	}
	for _, tt := range tests {
		if got := tt.people.Human(gitlab.User{Username: tt.user}); got != tt.want {
			t.Errorf("%s: Human(%s) = %v, want %v", tt.name, tt.user, got, tt.want)
		}
	}
}
