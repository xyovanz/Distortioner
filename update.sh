#!/usr/bin/env bash
# Pull latest Distortioner code, rebuild the Docker image, recreate the container.
# Invoked by admin /update or manually: ./update.sh [branch]
#
# For private HTTPS remotes, set GITHUB_TOKEN (or GH_TOKEN) — Contents:Read is enough.
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(pwd)"
BRANCH="${1:-${UPDATE_BRANCH:-${DISTORTIONER_UPDATE_BRANCH:-wip}}}"
LOG_DIR="${ROOT}/data"
LOG_FILE="${DISTORTIONER_UPDATE_LOG:-${LOG_DIR}/update.log}"
mkdir -p "${LOG_DIR}"

IMAGE="${DISTORTIONER_IMAGE:-distortioner:local}"
CONTAINER="${DISTORTIONER_CONTAINER_NAME:-distortioner}"
ENV_FILE="${DISTORTIONER_ENV_FILE:-distortioner.env}"
DATA_DIR="${DISTORTIONER_DATA_DIR:-}"

notify() {
  local text="$1"
  local token="${DISTORTIONER_BOT_TOKEN:-${TELEGRAM_BOT_TOKEN:-}}"
  local chat="${UPDATE_NOTIFY_CHAT_ID:-${DISTORTIONER_ADMIN_ID:-${TELEGRAM_ADMIN_ID:-}}}"
  if [[ -n "${token}" && -n "${chat}" ]]; then
    curl -fsS -X POST "https://api.telegram.org/bot${token}/sendMessage" \
      --data-urlencode "chat_id=${chat}" \
      --data-urlencode "text=${text}" \
      --data-urlencode "disable_web_page_preview=true" \
      >/dev/null 2>&1 || true
  fi
}

fail() {
  local text="$1"
  echo "❌ ${text}"
  notify "❌ Distortioner /update failed: ${text}"
  exit 1
}

git_fetch_branch() {
  local branch="$1"
  local token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
  local remote_url
  remote_url="$(git remote get-url origin 2>/dev/null || true)"

  if [[ -z "${remote_url}" ]]; then
    fail "git remote 'origin' is not configured"
  fi

  echo "📡 Remote: ${remote_url}"
  if [[ -n "${token}" ]]; then
    echo "🔑 Using GITHUB_TOKEN from env (${#token} chars)"
  else
    echo "🔑 No GITHUB_TOKEN/GH_TOKEN in env"
  fi

  if [[ "${remote_url}" == git@* || "${remote_url}" == ssh://* ]]; then
    if ! git fetch origin "refs/heads/${branch}:refs/remotes/origin/${branch}"; then
      fail "git fetch over SSH failed — check deploy key / ssh agent"
    fi
    return
  fi

  if [[ -n "${token}" ]]; then
    token="${token%\"}"
    token="${token#\"}"
    token="${token%\'}"
    token="${token#\'}"
    token="$(echo -n "${token}" | tr -d '[:space:]')"
    local basic
    basic="$(printf 'x-access-token:%s' "${token}" | base64 -w0 2>/dev/null || printf 'x-access-token:%s' "${token}" | base64)"
    if ! git -c "http.extraHeader=Authorization: Basic ${basic}" \
      fetch origin "refs/heads/${branch}:refs/remotes/origin/${branch}"; then
      fail "git fetch failed — PAT needs repo read access, or switch origin to SSH"
    fi
    return
  fi

  if ! git fetch origin "refs/heads/${branch}:refs/remotes/origin/${branch}"; then
    fail "git fetch failed (no credentials). Add GITHUB_TOKEN to env, or switch origin to SSH."
  fi
}

exec > >(tee -a "${LOG_FILE}") 2>&1

echo "======== $(date -Is) update start (branch=${BRANCH}) ========"
echo "📂 ${ROOT}"

if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi
if [[ -f "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${ENV_FILE}"
  set +a
fi

echo "📥 Fetching origin/${BRANCH}..."
git_fetch_branch "${BRANCH}"

if ! git show-ref --verify --quiet "refs/remotes/origin/${BRANCH}"; then
  fail "origin/${BRANCH} not found after fetch"
fi

LOCAL_SHORT="$(git rev-parse --short HEAD)"
REMOTE_SHORT="$(git rev-parse --short "origin/${BRANCH}")"
BEHIND="$(git rev-list --count HEAD.."origin/${BRANCH}")"
AHEAD="$(git rev-list --count "origin/${BRANCH}"..HEAD)"
HEAD_MSG="$(git log -1 --pretty=%s)"

echo "📍 HEAD ${LOCAL_SHORT}  origin/${BRANCH} ${REMOTE_SHORT}  behind=${BEHIND} ahead=${AHEAD}"

if [[ "${BEHIND}" == "0" ]]; then
  if [[ "${AHEAD}" != "0" ]]; then
    echo "✅ origin/${BRANCH} has no new commits (local is ${AHEAD} ahead). Skipping."
    notify "✅ Distortioner already has origin/${BRANCH} (${LOCAL_SHORT}: ${HEAD_MSG}). Local is ${AHEAD} commit(s) ahead — nothing to pull."
  else
    echo "✅ Already up to date."
    notify "✅ Distortioner already up to date: ${LOCAL_SHORT} (${HEAD_MSG})"
  fi
  echo "✅ No update needed at $(date -Is)"
  exit 0
fi

NEW_COMMITS="$(git log --oneline -10 HEAD.."origin/${BRANCH}")"
echo "📋 ${BEHIND} new commit(s) on origin/${BRANCH}:"
echo "${NEW_COMMITS}"
notify "🔄 Updating Distortioner: ${BEHIND} new commit(s) on origin/${BRANCH} (${LOCAL_SHORT} → ${REMOTE_SHORT})
${NEW_COMMITS}"

echo "💾 Backing up env files..."
cp -a .env .env.bak 2>/dev/null || true
cp -a "${ENV_FILE}" "${ENV_FILE}.bak" 2>/dev/null || true

echo "🔁 Resetting to origin/${BRANCH}..."
git reset --hard "origin/${BRANCH}"

echo "♻️ Restoring env files..."
for f in .env "${ENV_FILE}"; do
  if [[ -f "${f}.bak" ]]; then
    cp -a "${f}.bak" "${f}"
  fi
done

echo "🐳 Building ${IMAGE}..."
if ! docker build -t "${IMAGE}" .; then
  fail "docker build failed"
fi

if [[ -z "${DATA_DIR}" ]]; then
  fail "DISTORTIONER_DATA_DIR is not set (host path for -v …:/app/data)"
fi
if [[ ! -f "${ENV_FILE}" ]]; then
  fail "env file ${ENV_FILE} not found"
fi

echo "🔄 Recreating container ${CONTAINER}..."
docker stop "${CONTAINER}" >/dev/null 2>&1 || true
docker rm "${CONTAINER}" >/dev/null 2>&1 || true
docker run -d --restart unless-stopped \
  --name "${CONTAINER}" \
  --env-file "${ENV_FILE}" \
  -v "${DATA_DIR}:/app/data" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "${ROOT}:${ROOT}" \
  -w "${ROOT}" \
  "${IMAGE}"

HEAD_SHA="$(git rev-parse --short HEAD)"
HEAD_MSG="$(git log -1 --pretty=%s)"
notify "✅ Distortioner updated to ${HEAD_SHA} (${HEAD_MSG}). Container recreated."

echo "✅ Update complete at $(date -Is) (${HEAD_SHA})"
