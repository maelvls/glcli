package gitlab

import "testing"

func TestIsBotServiceAccountsAndInternalBots(t *testing.T) {
	for _, u := range []string{
		// Access tokens and "*-bot" accounts.
		"project_123_bot_abc123", "group_9_bot", "renovate-bot", "my_bot", "bot",
		// Service accounts, with GitLab's default prefixes.
		"service_account_group_12_ab12cd", "service_account_project_3_x", "service_account_6018816a18e515214e0c34c2b33523fc",
		// The users GitLab creates for itself, whatever the case.
		"ghost", "gitlab-bot", "GitLab-Security-Bot", "gitlab-security-bot", "GitLab-Admin-Bot", "gitlab-admin-bot",
		"alert-bot", "support-bot", "automation-bot", "Ghost",
	} {
		if !IsBot(u) {
			t.Errorf("%s should be a bot", u)
		}
	}
	for _, u := range []string{
		"mael.valais", "abbott", "robotnik", "renovated", "duolingo", "bob",
		// People called Le Bot: the "-bot" rule is case-sensitive.
		"Yann.Le-Bot", "Jan_Bot", "LE-BOT", "J-Bot", "Bot",
		// GitLab Duo reviews are comments the author waits for.
		"GitLabDuo", "gitlab-duo",
		// Names anyone can register: --ignore-author is there for them.
		"dependabot", "renovate", "dependabot[bot]",
		// The prefixes are case-sensitive and anchored.
		"Service_Account_Group_12_ab12cd", "Project_123_Bot_abc123", "my_service_account_1", "service_accountant",
		"ghostwriter", "gitlab-security-bot2", "ghoſt", "",
	} {
		if IsBot(u) {
			t.Errorf("%s should not be a bot", u)
		}
	}
}
