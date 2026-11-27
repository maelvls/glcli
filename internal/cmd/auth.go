package cmd

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/maelvls/glcli/internal/cli"
	"github.com/maelvls/glcli/internal/gitlab"
)

func newAuth(rt *cli.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Show which GitLab hosts and tokens glcli uses (from glab)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "List the hosts of glab's config, without calling GitLab (tokens are not printed)",
		Long: `List the hosts of glab's config and where the token of each comes from,
without calling GitLab. Tokens are never printed.

A host glab logged in to with OAuth has "token_kind": "oauth", and
"expires_at" when glab's config says when its token expires. glcli never
renews such a token: glab does, when it calls GitLab (for instance
"glab auth status --hostname <host>"), and glcli reads the new one from
glab's config, even in the middle of a wait command.

A host whose token is "missing" has "keyring": true when glab's config
says that glab keeps it in the keyring of the operating system (its
default since glab 1.111.0), where glcli does not read it. A hint says
what to do about a missing token, for such a host and for the default
host.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := rt.Config()
			if err != nil {
				return err
			}
			type host struct {
				Name  string `json:"name"`
				User  string `json:"user,omitempty"`
				Token string `json:"token"`
				// TokenKind is "oauth" for a host glab logged in to with
				// OAuth, and absent for an access token.
				TokenKind string `json:"token_kind,omitempty"`
				// ExpiresAt is when glab's config says the OAuth token
				// expires.
				ExpiresAt string `json:"expires_at,omitempty"`
				// Keyring says that the token is missing because glab
				// keeps it in the keyring of the operating system, where
				// glcli does not read it.
				Keyring bool   `json:"keyring,omitempty"`
				Default bool   `json:"default,omitempty"`
				APIURL  string `json:"api_url"`
			}
			var hosts []host
			def := cfg.Default()
			names := cfg.Names()
			if _, ok := cfg.Hosts[def]; !ok {
				names = append(names, def)
			}
			for _, n := range names {
				h, err := cfg.Host(n)
				status := "from " + h.TokenSource
				if err != nil {
					status = "missing"
				}
				row := host{Name: n, User: h.User, Token: status, Keyring: h.Keyring && err != nil, Default: n == def, APIURL: h.BaseURL() + "/api/v4"}
				// Why the token is missing, and what to do about it: for
				// the host glcli uses unless told otherwise, and for one
				// whose token is known to be in the keyring. Not for
				// every host without a token: glab's config comes with
				// gitlab.com in it, with an empty token, and someone who
				// only uses another instance has nothing to do about it.
				if err != nil && (n == def || h.Keyring) {
					rt.Hintf("%v.", err)
				}
				if h.OAuth {
					row.TokenKind = "oauth"
					if !h.Expiry.IsZero() {
						row.ExpiresAt = h.Expiry.Format(time.RFC3339)
						if err == nil && h.Expiry.Before(rt.Now()) {
							rt.Hintf("The OAuth token of %s expired at %s. glcli never renews it; glab does, when it calls GitLab: run `glab auth status --hostname %s` before the next glcli command.", n, row.ExpiresAt, n)
						}
					}
				}
				hosts = append(hosts, row)
			}
			return rt.Print(struct {
				Config string `json:"config"`
				Hosts  []host `json:"hosts"`
			}{cfg.Path, hosts}, "hosts")
		},
	}, &cobra.Command{
		Use:   "test",
		Short: "Check the token of a host (--host, or the default one) by asking GitLab who it belongs to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := rt.Client("")
			if err != nil {
				return err
			}
			var u gitlab.User
			if err := c.Get(cmd.Context(), "user", nil, &u); err != nil {
				return err
			}
			return rt.Print(struct {
				Host     string `json:"host"`
				Username string `json:"username"`
				Name     string `json:"name"`
			}{c.Host.Name, u.Username, u.Name})
		},
	})
	return cmd
}
