#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="${REPO_DIR:-/home/wlcb/devel/StudioB-UI}"
cd "${REPO_DIR}"

if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "Not a git repo: ${REPO_DIR}"
  exit 1
fi

# Ensure origin uses SSH (GitHub)
SSH_ORIGIN="${SSH_ORIGIN:-git@github.com:WLCB-LP/StudioB-UI.git}"
if git remote get-url origin >/dev/null 2>&1; then
  CUR="$(git remote get-url origin)"
  if [[ "${CUR}" != "${SSH_ORIGIN}" ]]; then
    echo "[git-publish] Setting origin to SSH: ${SSH_ORIGIN}"
    git remote set-url origin "${SSH_ORIGIN}"
  fi
else
  echo "[git-publish] Adding origin: ${SSH_ORIGIN}"
  git remote add origin "${SSH_ORIGIN}"
fi

MSG="${1:-}"
if [[ -z "${MSG}" ]]; then
  MSG="Studio B UI update $(date -Is)"
fi

echo "[git-publish] Status:"
git status --porcelain || true

git add -A

if git diff --cached --quiet; then
  echo "[git-publish] Nothing to commit."
  exit 0
fi

git commit -m "${MSG}"
git push

echo "[git-publish] Done."
