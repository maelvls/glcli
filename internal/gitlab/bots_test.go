package gitlab

import "testing"

func TestIsBotServiceAccountsAndKnownBots(t *testing.T) {
	for _, u := range []string{
		// Access tokens, "*-bot" accounts and the ghost user.
		"project_123_bot_abc123", "group_9_bot", "renovate-bot", "ghost", "gitlab-bot",
		// Service accounts, as GitLab names them by default.
		"service_account_group_12_ab12cd", "service_account_project_3_x", "service_account_6018816a18e515214e0c34c2b33523fc",
		// GitLab Duo code review.
		"GitLabDuo", "gitlab-duo",
		// Dependency updaters, and the name[bot] form.
		"dependabot", "renovate", "dependabot[bot]", "renovate[bot]",
		// Whatever the case.
		"Renovate", "Dependabot[Bot]", "GITLABDUO", "Service_Account_Group_12_ab12cd", "Project_123_Bot_abc123", "Renovate-Bot", "Ghost",
	} {
		if !IsBot(u) {
			t.Errorf("%s should be a bot", u)
		}
	}
	for _, u := range []string{
		"mael.valais", "abbott", "robotnik", "renovated", "duolingo", "bob",
		"my_service_account_1", "service_accountant", "gitlabduo2", "renovate.smith", "dependabots", "ghostwriter", "", "[bot]ched",
	} {
		if IsBot(u) {
			t.Errorf("%s should not be a bot", u)
		}
	}
}
