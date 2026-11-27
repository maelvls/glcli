package cmd

import (
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := rt.Config()
			if err != nil {
				return err
			}
			type host struct {
				Name    string `json:"name"`
				User    string `json:"user,omitempty"`
				Token   string `json:"token"`
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
				hosts = append(hosts, host{Name: n, User: h.User, Token: status, Default: n == def, APIURL: h.BaseURL() + "/api/v4"})
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
