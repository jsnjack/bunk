#!/usr/bin/env bash
# Stop hook: run `make check-all` when the code changed since the last run.
#
# The tree hash covers HEAD, uncommitted changes and untracked files (minus
# .claude). A green run stamps the tree, so a reply with no code changes
# costs nothing. A failure exits 2, which sends the log tail back to Claude.
# The same failing tree is not run again, so a suite Claude cannot fix does
# not block every later reply.
set -uo pipefail

cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0
git rev-parse --git-dir >/dev/null 2>&1 || exit 0
state=$(git rev-parse --absolute-git-dir)/claude-check-all
mkdir -p "$state"
cat >/dev/null # hook input; not needed

tree_hash() {
	{
		git rev-parse HEAD
		git diff HEAD --binary -- . ':!:.claude'
		git ls-files -z -o --exclude-standard -- . ':!:.claude' | xargs -0 -r sha256sum
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
if make check-all >"$log" 2>&1; then
	# make check runs goimports -w, so stamp the tree as it is after the run.
	tree_hash >"$state/green"
	rm -f "$state/failed"
	message "make check-all passed in $((SECONDS - start)) s"
	exit 0
fi
tree_hash >"$state/failed"
{
	echo "make check-all failed after $((SECONDS - start)) s (full log: $log)."
	echo "Fix it, then finish; or tell the user why it cannot pass. Last 60 lines:"
	tail -n 60 "$log"
} >&2
exit 2
