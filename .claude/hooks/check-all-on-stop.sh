#!/usr/bin/env bash
# Stop hook: run `make check-all` when the code changed since the last run.
#
# Claude Code and Copilot CLI run it from .claude/settings.json, Codex from
# .codex/hooks.json. All three take the same answer: JSON on stdout, where
# {"decision": "block", "reason": …} makes the agent go on with the reason
# (the log tail) as its next prompt.
#
# The tree hash covers HEAD, uncommitted changes and untracked files (minus
# .claude and .codex). A green run stamps the tree, so a reply with no code
# changes costs nothing. The same failing tree is not run again, so a suite
# the agent cannot fix does not block every later reply.
#
# Claude Code and Codex run Stop hooks at once, so bunk's "notify done"
# hook reports the turn done while the suite still runs. While make runs,
# this hook reports working, a second in so that it lands after that done;
# it reports done again only when the suite passes. Copilot runs its hooks
# one after another, so after a block it reports working again two seconds
# later, past a "notify done" that runs after it.
set -uo pipefail

# Codex runs hooks in the session's directory and sets no project variable.
cd "${CLAUDE_PROJECT_DIR:-$(dirname "$0")/../..}" || exit 0
git rev-parse --git-dir >/dev/null 2>&1 || exit 0
state=$(git rev-parse --absolute-git-dir)/claude-check-all
mkdir -p "$state"
input=$(cat) # hook input: its last_assistant_message is the done notice

# notify reports the agent's state to the bunk pane, as the hook snippets
# of docs/agent-hooks.md do.
notify() {
	local b
	[[ -n ${BUNK_NOTIFY_SESSION:-}${SSH_CONNECTION:-} ]] || return 0
	b=$(command -v bunk-gui || command -v bunk) || return 0
	"$b" notify "$@" >/dev/null 2>&1
}

tree_hash() {
	{
		git rev-parse HEAD
		git diff HEAD --binary -- . ':!:.claude' ':!:.codex'
		git ls-files -z -o --exclude-standard -- . ':!:.claude' ':!:.codex' | xargs -0 -r sha256sum
	} | sha256sum | cut -d' ' -f1
}

message() { jq -cn --arg m "$1" '{systemMessage: $m}'; }

tree=$(tree_hash)
[[ $(cat "$state/green" 2>/dev/null) == "$tree" ]] && exit 0
if [[ $(cat "$state/failed" 2>/dev/null) == "$tree" ]]; then
	message "make check-all still fails for this tree; log: $state/last.log"
	exit 0
fi

exec 9>"$state/lock"
if ! flock -n 9; then
	message "make check-all is already running in another session; skipped"
	exit 0
fi

log=$state/last.log
start=$SECONDS
(sleep 1 && notify working) </dev/null >/dev/null 2>&1 9>&- &
working=$!
if make check-all >"$log" 2>&1; then
	# make check runs goimports -w, so stamp the tree as it is after the run.
	tree_hash >"$state/green"
	rm -f "$state/failed"
	wait "$working"
	printf '%s' "$input" | notify done -
	message "make check-all passed in $((SECONDS - start)) s"
	exit 0
fi
tree_hash >"$state/failed"
reason=$(
	echo "make check-all failed after $((SECONDS - start)) s (full log: $log)."
	echo "Fix it, then finish; or tell the user why it cannot pass. Last 60 lines:"
	tail -n 60 "$log"
)
(sleep 2 && notify working) </dev/null >/dev/null 2>&1 9>&- &
jq -cn --arg r "$reason" '{decision: "block", reason: $r}'
exit 0
