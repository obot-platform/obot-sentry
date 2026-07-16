#!/usr/bin/env bash
# Build the MDM assets remotely: dispatch the build.yaml workflow on the
# current branch of the repo `origin` points at (your fork), wait for it
# to finish, and download the assembled assets into dist/mdm-assets.
#
# Usage: scripts/mdm-remote.sh [version]   # version numeric x.y.z, optional
set -euo pipefail

version="${1:-}"
branch="$(git branch --show-current)"
if [[ -z "$branch" ]]; then
	echo "detached HEAD; check out a branch first" >&2
	exit 1
fi

# Pin every gh call to the repo `origin` points at — gh's own default
# repo is often the upstream, which would 404 (no such workflow) or
# dispatch against the wrong repo.
repo="$(git remote get-url origin | sed -E 's#^(git@github\.com:|https://github\.com/)##; s#\.git$##')"

# workflow_dispatch runs the workflow file from the pushed ref, so the
# branch (with .github/workflows/build.yaml on it) must be on origin.
if ! git ls-remote --exit-code --heads origin "$branch" >/dev/null; then
	echo "branch $branch is not pushed to origin ($repo); commit and push it first" >&2
	exit 1
fi

args=(--ref "$branch")
if [[ -n "$version" ]]; then
	args+=(-f "version=$version")
fi

echo "Dispatching build.yaml on $repo@$branch ..."
gh workflow run build.yaml -R "$repo" "${args[@]}"

# The dispatch API returns before the run exists; poll for the run it
# created (the newest workflow_dispatch run on this branch).
runID=""
for _ in $(seq 30); do
	sleep 2
	runID="$(gh run list -R "$repo" --workflow build.yaml --branch "$branch" --event workflow_dispatch \
		--limit 1 --json databaseId --jq '.[0].databaseId' 2>/dev/null || true)"
	if [[ -n "$runID" && "$runID" != "null" ]]; then
		break
	fi
done
if [[ -z "$runID" || "$runID" == "null" ]]; then
	echo "could not find the dispatched run; check gh auth / the Actions tab" >&2
	exit 1
fi

echo "Watching run $runID ..."
gh run watch "$runID" -R "$repo" --exit-status

rm -rf dist/mdm-assets
mkdir -p dist
gh run download "$runID" -R "$repo" -n mdm-assets -D dist/mdm-assets
echo "dist/mdm-assets ready"
