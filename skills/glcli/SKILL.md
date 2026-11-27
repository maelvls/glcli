---
name: glcli
description: "GitLab CLI for agents: what waits on the user across all projects (review requests, replies, pushes on MRs they review, comments and failed pipelines on their MRs, to-do items), the compact state of a merge request with a cursor, the events since a cursor (pushes told apart from rebases, human comments, resolved threads, approvals, merges), its threads, a pipeline with its downstream pipelines and failed-job logs, and commands that wait for any of these. Use instead of polling glab in a loop."
---

# glcli

Use `glcli` from `$PATH`. If it is missing, tell the user: `go install github.com/maelvls/glcli/cmd/glcli@latest`, then `glcli skills install`. This skill is embedded in the binary: after an upgrade, `glcli skills install --overwrite` updates it.

`glcli --help` and each subcommand's `--help` are authoritative: they document every flag and the JSON each command prints. If a capability named here is absent from the installed help, report version skew, with the output of `glcli version` (a glcli that does not know `version` is older than this skill; `glcli skills list`, which only reads, says whether the skill installed in `~/.claude/skills`, or in `--dir`, differs from the binary's), instead of guessing.

glcli reads glab's hosts and tokens (`~/.config/glab-cli/config.yml`): if a token is missing or rejected, tell the user to run `glab auth login --hostname <host>`. `glcli auth status` lists the hosts without calling GitLab.

## Safety

glcli v1 only reads. For writes (comments, replies, resolving threads, approving), use `glab` and follow the user's instructions about it. Never merge.

## Workflow

1. Name a merge request by its URL (quote it), `group/project!123`, or `!123` inside a checkout of the project. A pipeline is its URL, or the merge request whose head pipeline you want.
2. Choose the narrowest command: `inbox` for what waits on the user, `mr get` for one merge request, `mr discussions` for its threads, `pipeline get` for CI.
3. To wait for something, use a wait command, never your own `sleep` loop: `inbox wait`, `mr wait`, `pipeline wait`. Run it in the background (it has no time limit unless `--timeout`), and you are told when it exits.
4. Never write `glcli … 2>&1 | jq …`: errors are plain text on stderr and jq hides them. Write `glcli … 2>err.txt | jq …` and read `err.txt` when the exit code is not 0 or the output is empty.
5. Read `warnings` and `hints` in the JSON: they say what was left out and what to run next.

## Use cases

**Was I asked for a review, or answered, anywhere?** `glcli inbox` lists, newest first, the open merge requests I review (`review_requested`, `new_commits` after my review, `new_replies` in my threads or mentioning me), my own merge requests (`new_comments`, `pipeline_failed`, `ready_to_merge`), and my pending to-do items (`todo:<action>`; merged into the merge request they are about). It looks at the last 14 days unless `--since`. To be told of the next one: `glcli inbox wait --since <checked_at of the previous inbox>`, in the background.

**Watch a merge request for comments or pushes.** `glcli mr get <mr>` prints the state and a `cursor`. Then `glcli mr wait <mr> --cursor <cursor>` in the background: it exits on a push that changes the code, a comment by someone else, a resolved or reopened thread, an approval, a merge or a close (`--on` changes the list; my own actions do not count unless `--include-mine`). It prints every `event` since the cursor and `mr.cursor`, the cursor to wait again from: pass it to the next `mr wait` so that nothing is missed. On `--timeout` it prints `timed_out: true` and the cursor it started from.

**Was that push just a rebase?** A `push` event has `rebase_only: true` when the changes of the merge request are the same as before (rebase, reword): no need to review again. Such pushes do not end `mr wait` unless `--on rebase`. `rebase_only` is absent when GitLab could not compute the diff.

**Reply to the new comments.** Each `comment` event has the `discussion_id` to reply to, the `body`, the `file`/`line` of a diff comment, and `in_thread` (the first note of the thread it answers, `mine: true` when I started it). `glcli mr discussions <mr> --unresolved` (or `--mine`, or `--cursor <cursor>` for threads with new notes) lists the threads with their notes.

**Wait for CI after a push.** `glcli pipeline wait <mr>` in the background: it waits for GitLab to create the pipeline of the head commit, then for it and its downstream pipelines (trigger jobs such as devstack e2e) to finish, and prints `failed_jobs` with the end of each log (`--log-lines`). Exit code 0 whatever the pipeline status: read `pipeline.status` and `failed_jobs`.

**Do something once a merge request is merged.** `glcli mr wait <mr> --on state` exits when it is merged or closed; the `state` event has `state`, `by` and the merge `sha`.

## Non-obvious behaviour

- Output is one line of JSON (indented only on a terminal); empty fields are left out, but top-level lists (`items`, `events`, `discussions`, `failed_jobs`) are always present.
- Comments by bots (`project_123_bot_…`, `*-bot`) and system notes never count as human comments; `--ignore-author` adds usernames.
- Commands are cut after 30 s (exit 124; `GLCLI_COMMAND_TIMEOUT_MS`), except the wait commands. The wait commands keep trying while GitLab cannot be reached (closed lid, VPN down) and stop on any other error. Exit 130 when interrupted.
- `--host` picks another instance (such as `code.pan.run`) for `inbox` and short references; a URL carries its host.
- `GLCLI_DEBUG=1` traces each API call with its duration on stderr.
