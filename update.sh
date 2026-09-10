#!/usr/bin/env bash
# Pull latest Distortioner code, rebuild the Docker image, recreate the container.
# Invoked by admin /update or manually: ./update.sh [branch]
#
# For private HTTPS remotes, set GITHUB_TOKEN (or GH_TOKEN) — Contents:Read is enough.
set -euo pipefail

cd "$(dirname "$0")"
ROOT="$(pwd)"
ENV_FILE="${DISTORTIONER_ENV_FILE:-distortioner.env}"
LOG_DIR="${ROOT}/data"
mkdir -p "${LOG_DIR}"

# Load env BEFORE reading DISTORTIONER_* into locals (bug: early DATA_DIR= stayed empty).
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

BRANCH="${1:-${UPDATE_BRANCH:-${DISTORTIONER_UPDATE_BRANCH:-master}}}"
LOG_FILE="${DISTORTIONER_UPDATE_LOG:-${LOG_DIR}/update.log}"
IMAGE="${DISTORTIONER_IMAGE:-distortioner:local}"
CONTAINER="${DISTORTIONER_CONTAINER_NAME:-distortioner}"
ENV_FILE="${DISTORTIONER_ENV_FILE:-distortioner.env}"
if [[ "${ENV_FILE}" != /* ]]; then
  ENV_FILE="${ROOT}/${ENV_FILE}"
fi
# Docker *host* path for -v HOST:/app/data (only use /app/data if that path exists on the host).
DATA_DIR="${DISTORTIONER_DATA_DIR:-}"
DATA_DIR="${DATA_DIR//$'\r'/}"
DATA_DIR="$(echo -n "${DATA_DIR}" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//')"

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

ensure_git_origin() {
  if ! command -v git >/dev/null 2>&1; then
    fail "git is not installed here (needed for /update). Rebuild the image or run ./update.sh on the host."
  fi
  if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    fail "${ROOT} is not a git checkout (is .git mounted?). Mount the full repo, e.g. -v /root/Distortioner:/root/Distortioner"
  fi
  if git remote get-url origin >/dev/null 2>&1; then
    return 0
  fi
  local url="${DISTORTIONER_GIT_REMOTE:-https://github.com/xyovanz/Distortioner.git}"
  echo "⚠️ origin missing — adding ${url}"
  if ! git remote add origin "${url}"; then
    fail "could not add git remote origin (${url})"
  fi
}

git_fetch_branch() {
  local branch="$1"
  local token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
  local remote_url

  ensure_git_origin
  remote_url="$(git remote get-url origin)"

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
if ! command -v docker >/dev/null 2>&1; then
  fail "docker CLI is not installed here (needed for /update). Rebuild the image or run ./update.sh on the host."
fi
if ! docker build -t "${IMAGE}" .; then
  fail "docker build failed"
fi

if [[ -z "${DATA_DIR}" ]]; then
  fail "DISTORTIONER_DATA_DIR is not set. Put the *host* path in distortioner.env (the left side of -v HOST:/app/data)."
fi
if [[ ! -f "${ENV_FILE}" ]]; then
  fail "env file ${ENV_FILE} not found"
fi

# Never `docker stop` this container from inside it: the daemon waits for us to
# exit while we wait for `docker stop` — then SIGKILL leaves the box stopped and
# the following `docker run` never runs (that's why /update "hangs" and needs a
# manual recreate). Schedule a sibling helper instead.
in_docker() {
  [[ -f /.dockerenv ]] && return 0
  grep -qE '/docker|/lxc|/containerd' /proc/1/cgroup 2>/dev/null && return 0
  return 1
}

in_target_container() {
  [[ -S /var/run/docker.sock ]] || return 1
  local id name
  id="$(cat /etc/hostname 2>/dev/null || true)"
  [[ -n "${id}" ]] || return 1
  name="$(docker inspect --format '{{.Name}}' "${id}" 2>/dev/null || true)"
  name="${name#/}"
  [[ "${name}" == "${CONTAINER}" ]]
}

write_run_container() {
  # Keep workdir /app so SQLite uses the /app/data volume. Mount the repo so /update can run update.sh.
  cat <<EOF
docker run -d --restart unless-stopped \\
  --name $(printf '%q' "${CONTAINER}") \\
  --env-file $(printf '%q' "${ENV_FILE}") \\
  -e DISTORTIONER_UPDATE_SCRIPT=$(printf '%q' "${ROOT}/update.sh") \\
  -v $(printf '%q' "${DATA_DIR}"):/app/data \\
  -v /var/run/docker.sock:/var/run/docker.sock \\
  -v $(printf '%q' "${ROOT}"):$(printf '%q' "${ROOT}") \\
  -w /app \\
  --entrypoint /app/distortioner \\
  $(printf '%q' "${IMAGE}") || { echo "❌ docker run failed"; exit 1; }
EOF
}

HEAD_SHA="$(git rev-parse --short HEAD)"
HEAD_MSG="$(git log -1 --pretty=%s)"

echo "🔄 Recreating container ${CONTAINER}..."
if in_target_container || in_docker; then
  HELPER_NAME="${CONTAINER}-updater"
  HELPER_SCRIPT="${LOG_DIR}/.update-recreate.sh"
  docker rm -f "${HELPER_NAME}" >/dev/null 2>&1 || true
  cat > "${HELPER_SCRIPT}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
exec >>$(printf '%q' "${LOG_FILE}") 2>&1
echo "======== \$(date -Is) helper recreate start ========"
sleep 2
docker stop -t 20 $(printf '%q' "${CONTAINER}") >/dev/null 2>&1 || true
docker rm -f $(printf '%q' "${CONTAINER}") >/dev/null 2>&1 || true
$(write_run_container)
TOKEN=$(printf '%q' "${DISTORTIONER_BOT_TOKEN:-${TELEGRAM_BOT_TOKEN:-}}")
CHAT=$(printf '%q' "${UPDATE_NOTIFY_CHAT_ID:-${DISTORTIONER_ADMIN_ID:-${TELEGRAM_ADMIN_ID:-}}}")
TEXT=$(printf '%q' "✅ Distortioner updated to ${HEAD_SHA} (${HEAD_MSG}). Container recreated.")
if [[ -n "\${TOKEN}" && -n "\${CHAT}" ]]; then
  curl -fsS -X POST "https://api.telegram.org/bot\${TOKEN}/sendMessage" \\
    --data-urlencode "chat_id=\${CHAT}" \\
    --data-urlencode "text=\${TEXT}" \\
    --data-urlencode "disable_web_page_preview=true" \\
    >/dev/null 2>&1 || true
fi
echo "✅ Helper recreate complete at \$(date -Is) (${HEAD_SHA})"
EOF
  chmod +x "${HELPER_SCRIPT}"
  if ! docker run -d --rm --name "${HELPER_NAME}" \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "${ROOT}:${ROOT}" \
    -w "${ROOT}" \
    --entrypoint /bin/bash \
    "${IMAGE}" \
    "${HELPER_SCRIPT}"; then
    fail "could not start recreate helper (container was not stopped)"
  fi
  echo "🚀 Recreate helper ${HELPER_NAME} started (will stop this container)"
  notify "🔄 Distortioner image built (${HEAD_SHA}). Recreating container…"
  echo "✅ Update scheduled at $(date -Is) (${HEAD_SHA}); helper will recreate the container"
  exit 0
fi

docker stop -t 20 "${CONTAINER}" >/dev/null 2>&1 || true
docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true
eval "$(write_run_container)"

notify "✅ Distortioner updated to ${HEAD_SHA} (${HEAD_MSG}). Container recreated."

echo "✅ Update complete at $(date -Is) (${HEAD_SHA})"
