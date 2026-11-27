# glcli

A GitLab CLI for AI agents. It prints compact JSON, and it has commands
that wait for what an agent is usually waiting on: a review request, a
reply, a push after a review, a merge, a pipeline.

It reuses glab's hosts and tokens (`~/.config/glab-cli/config.yml`), so
there is nothing to log in to.

```sh
glcli inbox                        # what waits on me across all projects
glcli inbox wait                   # block until something new waits on me
glcli mr get <mr>                  # state of a merge request, with a cursor
glcli mr wait <mr> --cursor <c>    # block until a push, comment, resolve, approval or merge
glcli mr discussions <mr>          # threads
glcli pipeline wait <mr>           # block until the head pipeline and its downstream ones finish
```

A merge request is its URL, `group/project!123`, or `!123` inside a git
checkout of the project.

## Why

Agents that review or babysit merge requests kept rewriting the same
polling scripts around `glab`: filter out bots and myself, find notes
newer than a timestamp, wait for the pipeline of the commit just pushed,
and re-review after a force-push that turned out to be a plain rebase.
glcli does those once:

- `mr wait` computes events against a cursor, so nothing that happens
  between two commands is lost, and tells a rebase from a change by
  comparing the patch of the merge request before and after the push.
- `inbox` merges the to-do list with the merge requests I review or
  authored, and says why each one waits on me.
- `pipeline wait` follows trigger jobs into downstream pipelines and
  prints the end of the logs of the failed jobs.

## Install

```sh
go install github.com/maelvls/glcli/cmd/glcli@latest
glcli skills install    # the agent skill, embedded in the binary, into ~/.claude/skills/glcli
```

After upgrading, `glcli skills install --overwrite` updates the skill.
`--dir` installs it elsewhere (a project's `.claude/skills`, another
agent's skills directory). From a checkout, `make install` does both steps.

To have the skill follow a checkout instead of a copy (`skills install`
then leaves the symlink alone):

```sh
ln -s "$PWD/skills/glcli" ~/.claude/skills/glcli
```

## Status

v1 reads and waits. Writes (comment, reply, resolve, approve) are next.
