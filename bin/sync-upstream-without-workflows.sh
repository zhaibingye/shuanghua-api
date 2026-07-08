#!/usr/bin/env bash
set -euo pipefail

remote="${1:-upstream}"
branch="${2:-main}"
target="${remote}/${branch}"
workflows_dir=".github/workflows"

if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "Working tree is not clean. Commit or stash local changes before syncing." >&2
  exit 1
fi

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

if git rev-parse --verify "HEAD:${workflows_dir}" >/dev/null 2>&1; then
  mkdir -p "${tmp_dir}/.github"
  git archive HEAD "${workflows_dir}" | tar -x -C "${tmp_dir}"
fi

git fetch "${remote}" "${branch}"

set +e
git merge --no-commit --no-ff "${target}"
merge_status=$?
set -e

# Keep this fork's workflow files exactly as they were before the upstream merge.
git rm -r --ignore-unmatch "${workflows_dir}" >/dev/null 2>&1 || true
if [ -d "${tmp_dir}/${workflows_dir}" ]; then
  mkdir -p .github
  cp -a "${tmp_dir}/${workflows_dir}" .github/
  git add "${workflows_dir}"
fi
git add -u "${workflows_dir}" >/dev/null 2>&1 || true

remaining_conflicts="$(git diff --name-only --diff-filter=U | grep -v "^${workflows_dir}/" || true)"
if [ -n "${remaining_conflicts}" ]; then
  echo "Upstream sync still has non-workflow conflicts:" >&2
  echo "${remaining_conflicts}" >&2
  echo "Resolve them manually, then commit the merge." >&2
  exit 1
fi

if [ "${merge_status}" -ne 0 ]; then
  echo "Workflow conflicts were resolved by restoring ${workflows_dir} from HEAD."
fi

echo "Upstream sync staged. Review with 'git status' and 'git diff --cached', then commit."
