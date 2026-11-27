SKILLS_DIR ?= $(HOME)/.claude/skills

.PHONY: install install-bin install-skill test

# Installs the binary into $(go env GOBIN) or ~/go/bin, and the agent skill
# into $(SKILLS_DIR).
install: install-bin install-skill

install-bin:
	go install ./cmd/glcli

# The skills are embedded in the binary: "glcli skills install" writes them.
# A symlinked skill (see the README) follows the checkout and is left alone.
install-skill:
	go run ./cmd/glcli skills install --overwrite --dir "$(SKILLS_DIR)"

test:
	go test ./...
