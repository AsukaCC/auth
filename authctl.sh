#!/bin/sh
# Offline release lifecycle. Only packages/ and .deployment/ are managed here.
set -eu
umask 077
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$ROOT"
STATE="$ROOT/.deployment"
RECOVER_ON_EXIT=0
die() { echo "错误：$*" >&2; exit 1; }
valid_version() {
  case "$1" in ''|*[!0-9a-zA-Z.-]*) return 1 ;; esac
  printf '%s\n' "$1" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$'
}
digest() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}
field() { sed -n "${2}p" "$1"; }
compose() {
  compose_file=$1; compose_image=$2; shift 2
  AUTH_IMAGE="$compose_image" docker compose --project-name "$PROJECT" --env-file "$ROOT/.env" \
    --project-directory "$ROOT" -f "$compose_file" "$@"
}
start() {
  # A web job must never recreate or stop its own deployment agent.
  compose "$STATE/versions/$1/compose.yaml" "$2" up -d --no-deps --no-build --pull never --wait --wait-timeout 120 auth
}
ensure_agent() {
  # Jobs originate in the already running agent; only the host manages it.
  [ "${AUTH_DEPLOY_AGENT_REQUEST:-false}" != true ] || return 0
  agent_image=auth-center-deploy-agent:local
  if [ -f "$ROOT/deploy-agent/image.id" ]; then
    agent_image=$(cat "$ROOT/deploy-agent/image.id")
    printf '%s\n' "$agent_image" | grep -Eq '^sha256:[0-9a-f]{64}$' || die "代理镜像记录无效。"
    [ -f "$ROOT/deploy-agent/image.tar" ] || die "缺少部署代理镜像，请使用完整发行包。"
    docker load -i "$ROOT/deploy-agent/image.tar" >/dev/null
  fi
  docker image inspect "$agent_image" >/dev/null || die "部署代理镜像不存在，请使用完整发行包或先构建 Dockerfile.agent。"
  # Older releases used a separate Compose project. Retire its agent before
  # starting the default service, retaining the state directory and socket volume.
  legacy_agents=$(docker ps -q --filter "label=com.docker.compose.project=${PROJECT}-deploy" --filter label=com.docker.compose.service=deploy-agent)
  if [ -n "$legacy_agents" ]; then
    docker stop --time 1260 $legacy_agents >/dev/null
  fi
  AUTH_DEPLOY_AGENT_IMAGE="$agent_image" AUTH_IMAGE="${CURRENT_ID:-auth-center:local}" \
    docker compose --project-name "$PROJECT" --env-file "$ROOT/.env" --project-directory "$ROOT" \
    -f "$ROOT/compose.yaml" up -d --no-deps --no-build --pull never --wait --wait-timeout 90 deploy-agent \
    || die "部署代理未就绪，请检查 deploy-agent 日志；数据和当前应用镜像未删除。"
}
stop() { compose "$ROOT/compose.yaml" "${1:-auth-center:local}" stop auth; }
read_state() {
  CURRENT=; CURRENT_ID=; PREVIOUS=; PREVIOUS_ID=
  if [ -f "$STATE/state" ]; then
    CURRENT=$(field "$STATE/state" 1); CURRENT_ID=$(field "$STATE/state" 2)
    PREVIOUS=$(field "$STATE/state" 3); PREVIOUS_ID=$(field "$STATE/state" 4)
  fi
}
write_state() {
  printf '%s\n%s\n%s\n%s\n' "$1" "$2" "$3" "$4" > "$STATE/state.tmp"
  mv "$STATE/state.tmp" "$STATE/state"
}
image_schema() {
  detected=$(docker image inspect --format '{{index .Config.Labels "io.authcenter.schema-version"}}' "$1") || return 1
  case "$detected" in 1|2) printf '%s\n' "$detected" ;; ''|'<no value>') printf '1\n' ;; *) return 1 ;; esac
}
recover() {
  read_state
  floor=1
  [ ! -f "$STATE/schema-floor" ] || floor=$(cat "$STATE/schema-floor")
  case "$floor" in 1|2) ;; *) echo '结构保护记录无效，停止恢复。' >&2; return 1 ;; esac
  if [ -n "$CURRENT" ]; then
    current_schema=$(image_schema "$CURRENT_ID") || return 1
    if [ "$current_schema" -lt "$floor" ]; then
      # A migration may already have committed. Never launch the old binary or
      # restore a backup automatically: retry the recorded new image forward.
      [ -f "$STATE/pending" ] || { echo '禁止跨结构降级，请使用兼容当前数据库的新镜像。' >&2; return 1; }
      forward=$(field "$STATE/pending" 1)
      valid_version "$forward" || return 1
      forward_id=$(cat "$STATE/versions/$forward/image") || return 1
      forward_schema=$(image_schema "$forward_id") || return 1
      [ "$forward_schema" -ge "$floor" ] || return 1
      echo "数据库可能已升级，仅向前恢复版本 $forward …"
      start "$forward" "$forward_id" || return 1
      write_state "$forward" "$forward_id" "$CURRENT" "$CURRENT_ID"
      rm -f "$STATE/pending"
      return 0
    fi
    echo "恢复已记录版本 $CURRENT …"
    start "$CURRENT" "$CURRENT_ID" || return 1
  else
    stop || return 1
  fi
  rm -f "$STATE/pending"
}
finish() {
  result=$?
  trap - EXIT HUP INT TERM
  if [ "$RECOVER_ON_EXIT" = 1 ] && [ -f "$STATE/pending" ]; then
    if ! recover; then
      echo "自动恢复未成功；保留数据与备份。修复原因后执行 ./authctl.sh recover。" >&2
      result=1
    fi
  fi
  rmdir "$STATE/operation.lock"
  exit "$result"
}
prune_packages() {
  # Never remove user backups, external downloads or arbitrary Docker images.
  for meta in "$ROOT"/packages/*.meta; do
    [ -f "$meta" ] || continue
    version=$(basename "$meta" .meta)
    valid_version "$version" || continue
    [ "$version" != "$CURRENT" ] && [ "$version" != "$PREVIOUS" ] || continue
    rm -f "$ROOT/packages/$version.tar" "$meta"
  done
}
load_release() {
  if [ -n "${AUTH_TARGET_VERSION:-}" ]; then TARGET=$AUTH_TARGET_VERSION
  else
    [ -f "$ROOT/RELEASE" ] || die "缺少 RELEASE，请解压完整发布包。"
    TARGET=$(cat "$ROOT/RELEASE")
  fi
  valid_version "$TARGET" || die "发布版本格式无效。"
  meta="$ROOT/packages/$TARGET.meta"
  [ -f "$meta" ] && [ -f "$ROOT/packages/$TARGET.tar" ] || die "安装包不完整。"
  TARGET_ID=$(field "$meta" 1)
  [ -z "${AUTH_EXPECT_TARGET_ID:-}" ] || [ "$TARGET_ID" = "$AUTH_EXPECT_TARGET_ID" ] || die "目标镜像已变化，服务未变更。"
  expected=$(field "$meta" 2)
  schema=$(field "$meta" 3)
  printf '%s\n' "$TARGET_ID" | grep -Eq '^sha256:[0-9a-f]{64}$' || die "镜像 ID 无效。"
  case "$schema" in 1|2) ;; *) die "此管理工具仅支持数据库版本 1 和 2。" ;; esac
  [ "$(digest "$ROOT/packages/$TARGET.tar")" = "$expected" ] || die "安装包校验失败，服务未变更。"
  if [ -f "$STATE/versions/$TARGET/image" ]; then
    [ "$(cat "$STATE/versions/$TARGET/image")" = "$TARGET_ID" ] || die "同一版本号对应不同镜像，请发布新版本号。"
  fi
  docker load -i "$ROOT/packages/$TARGET.tar" >/dev/null
  [ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$TARGET_ID")" = "$TARGET" ] || die "镜像版本与安装包不一致。"
  [ "$(image_schema "$TARGET_ID")" = "$schema" ] || die "镜像数据库版本与安装包不一致。"
  mkdir -p "$STATE/versions/$TARGET"
  printf '%s\n' "$TARGET_ID" > "$STATE/versions/$TARGET/image"
  if [ ! -f "$STATE/versions/$TARGET/compose.yaml" ]; then
    cp "$ROOT/compose.yaml" "$STATE/versions/$TARGET/compose.yaml"
  fi
  compose "$STATE/versions/$TARGET/compose.yaml" "$TARGET_ID" config --quiet
}

ACTION=${1:-status}
case "$ACTION" in install|update|rollback|adopt|recover|status) ;; *) die "用法：$0 install|update|rollback|adopt|recover|status" ;; esac
command -v docker >/dev/null 2>&1 || die "需要 Docker 和 Docker Compose。"
docker info >/dev/null 2>&1 || die "Docker 不可用。"
mkdir -p "$STATE"
mkdir "$STATE/operation.lock" 2>/dev/null || die "此目录已有发布操作；若进程已中断，确认没有操作运行后删除 .deployment/operation.lock 再执行 recover。"
trap finish EXIT
trap 'exit 130' HUP INT TERM
PROJECT=${AUTH_PROJECT_NAME:-auth-center}
printf '%s\n' "$PROJECT" | grep -Eq '^[a-z0-9][a-z0-9_-]*$' || die "项目名无效。"
if [ -f "$STATE/project" ]; then
  PROJECT=$(cat "$STATE/project")
else
  printf '%s\n' "$PROJECT" > "$STATE/project"
fi
read_state
if [ "$ACTION" = status ]; then
  printf '当前版本：%s\n可回退版本：%s\n' "${CURRENT:-未纳入发布管理}" "${PREVIOUS:-无}"
  [ ! -f "$STATE/pending" ] || echo '存在中断的操作，请执行 recover。'
  # Status must not trigger the recovery trap.
  trap 'rmdir "$STATE/operation.lock"' EXIT
  docker ps -a --filter "label=com.docker.compose.project=$PROJECT" --filter label=com.docker.compose.service=auth \
    --format '容器：{{.Names}}  {{.Status}}'
  exit 0
fi
[ -f "$ROOT/.env" ] || die "请先复制 .env.docker.example 为 .env 并填写配置。"
[ "$ACTION" = recover ] || [ ! -f "$STATE/pending" ] || die "存在中断操作，请先执行 recover。"

containers=$(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT" --filter label=com.docker.compose.service=auth)
[ "$(printf '%s\n' "$containers" | awk 'NF {n++} END {print n+0}')" -le 1 ] || die "检测到多实例，请先恢复单实例部署。"
VOLUME="${PROJECT}_auth-data"
if [ -n "$containers" ]; then
  mounted=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$containers")
  [ "$mounted" = "$VOLUME" ] || die "数据卷与标准部署不一致，拒绝自动替换。"
  actual_id=$(docker inspect --format '{{.Image}}' "$containers")
  [ "$ACTION" = recover ] || [ -z "$CURRENT" ] || [ "$actual_id" = "$CURRENT_ID" ] || die "运行镜像与发布记录不一致，请先核对手工部署变更。"
fi
[ -z "${AUTH_EXPECT_CURRENT_ID:-}" ] || [ "${actual_id:-}" = "$AUTH_EXPECT_CURRENT_ID" ] || die "当前镜像已变化，操作未执行。"
if [ "$ACTION" = recover ]; then
  recover || die "恢复失败，请检查容器日志。"
  ensure_agent
  exit 0
fi
case "$ACTION" in
  install)
    [ -z "$CURRENT" ] && [ -z "$containers" ] || die "已安装，请使用 update，不能重复安装。"
    if docker volume inspect "$VOLUME" >/dev/null 2>&1 && [ ! -f "$STATE/initialized" ]; then
      die "发现已有数据卷。请启动原部署后使用 adopt 接管，避免重复初始化。"
    fi
    load_release
    ;;
  adopt)
    [ -z "$CURRENT" ] && [ -n "$containers" ] || die "adopt 仅用于接管现有的单实例 Compose 部署。"
    old_schema=$(docker image inspect --format '{{index .Config.Labels "io.authcenter.schema-version"}}' "$actual_id")
    case "$old_schema" in 1|2|''|'<no value>') ;; *) die "原镜像数据库结构版本不兼容，拒绝自动接管。" ;; esac
    load_release
    CURRENT=legacy; CURRENT_ID=$actual_id
    mkdir -p "$STATE/versions/legacy"
    cp "$ROOT/compose.yaml" "$STATE/versions/legacy/compose.yaml"
    ;;
  update)
    [ -n "$CURRENT" ] || die "尚未安装；已有部署请使用 adopt，新部署使用 install。"
    load_release
    if [ "$TARGET" = "$CURRENT" ]; then
      ensure_agent
      prune_packages
      echo "已经是版本 ${CURRENT}，无需重复安装。"
      exit 0
    fi
    ;;
  rollback)
    [ -n "$PREVIOUS" ] || die "没有可回退版本。"
    [ -z "${AUTH_EXPECT_PREVIOUS_ID:-}" ] || [ "$PREVIOUS_ID" = "$AUTH_EXPECT_PREVIOUS_ID" ] || die "可回退镜像已变化，操作未执行。"
    TARGET=$PREVIOUS; TARGET_ID=$PREVIOUS_ID
    docker image inspect "$TARGET_ID" >/dev/null || die "回退镜像不存在，请重新导入对应安装包。"
    ;;
esac

target_schema=$(image_schema "$TARGET_ID") || die "目标镜像结构版本无效。"
floor=1
[ ! -f "$STATE/schema-floor" ] || floor=$(cat "$STATE/schema-floor")
case "$floor" in 1|2) ;; *) die "结构保护记录无效，拒绝更新。" ;; esac
current_schema=$floor
if [ -n "$CURRENT_ID" ]; then
  current_schema=$(image_schema "$CURRENT_ID") || die "当前镜像结构版本无效。"
  [ "$current_schema" -le "$floor" ] || floor=$current_schema
fi
[ "$target_schema" -ge "$floor" ] || die "禁止从结构版本 ${floor} 降级到 ${target_schema}；数据未变更。"
if [ -n "$CURRENT_ID" ] && [ "$target_schema" -gt "$current_schema" ]; then
  [ "${AUTH_ALLOW_SCHEMA_UPGRADE:-false}" = true ] || die "此操作将迁移数据库到结构版本 ${target_schema}，不能回退旧结构镜像。核对备份方案后使用 AUTH_ALLOW_SCHEMA_UPGRADE=true 重试。"
fi

ensure_agent
# Record an adopted deployment only after all migration guards have passed.
[ "$ACTION" != adopt ] || write_state "$CURRENT" "$CURRENT_ID" "" ""
# Journal exists before stopping. Recovery respects the persisted schema floor.
printf '%s\n' "$TARGET" > "$STATE/pending"
RECOVER_ON_EXIT=1
touch "$STATE/initialized"
stop "${CURRENT_ID:-$TARGET_ID}"
if docker volume inspect "$VOLUME" >/dev/null 2>&1; then
  [ -z "$(docker ps -q --filter "volume=$VOLUME")" ] || die "仍有其他容器使用数据卷，取消更新。"
  mkdir -p "$ROOT/backups"
  BACKUP="$ROOT/backups/$(date -u +%Y%m%dT%H%M%SZ)-$$.tar.gz"
  echo "备份数据到 $BACKUP …"
  docker run --rm --network none --read-only --user 10001:10001 \
    --mount "type=volume,src=$VOLUME,dst=/data,readonly" --entrypoint tar \
    "${CURRENT_ID:-$TARGET_ID}" -czf - -C /data . > "$BACKUP.partial"
  mv "$BACKUP.partial" "$BACKUP"
fi
# Persist the floor before the new binary can touch the database. Even a failed
# health check must not trigger recovery into an incompatible older schema.
printf '%s\n' "$target_schema" > "$STATE/schema-floor.tmp"
mv "$STATE/schema-floor.tmp" "$STATE/schema-floor"
echo "部署版本 $TARGET …"
start "$TARGET" "$TARGET_ID" || die "版本启动失败，正在按结构兼容规则恢复。"
write_state "$TARGET" "$TARGET_ID" "$CURRENT" "$CURRENT_ID"
rm -f "$STATE/pending"
read_state
prune_packages
echo "完成：当前 ${CURRENT}，上一版本 ${PREVIOUS:-无}（仅相同结构可回退）。托管安装包保留这两个版本；数据备份单独保留。"
