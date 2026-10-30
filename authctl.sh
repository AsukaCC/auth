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
  compose "$STATE/versions/$1/compose.yaml" "$2" up -d --no-build --pull never --wait --wait-timeout 120 auth
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
recover() {
  read_state
  if [ -n "$CURRENT" ]; then
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
  [ -f "$ROOT/RELEASE" ] || die "缺少 RELEASE，请解压完整发布包。"
  TARGET=$(cat "$ROOT/RELEASE")
  valid_version "$TARGET" || die "发布版本格式无效。"
  meta="$ROOT/packages/$TARGET.meta"
  [ -f "$meta" ] && [ -f "$ROOT/packages/$TARGET.tar" ] || die "安装包不完整。"
  TARGET_ID=$(field "$meta" 1)
  expected=$(field "$meta" 2)
  schema=$(field "$meta" 3)
  printf '%s\n' "$TARGET_ID" | grep -Eq '^sha256:[0-9a-f]{64}$' || die "镜像 ID 无效。"
  [ "$schema" = 1 ] || die "此管理工具只支持数据库版本 1；跨结构升级需要专门迁移流程。"
  [ "$(digest "$ROOT/packages/$TARGET.tar")" = "$expected" ] || die "安装包校验失败，服务未变更。"
  if [ -f "$STATE/versions/$TARGET/image" ]; then
    [ "$(cat "$STATE/versions/$TARGET/image")" = "$TARGET_ID" ] || die "同一版本号对应不同镜像，请发布新版本号。"
  fi
  docker load -i "$ROOT/packages/$TARGET.tar" >/dev/null
  [ "$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$TARGET_ID")" = "$TARGET" ] || die "镜像版本与安装包不一致。"
  [ "$(docker image inspect --format '{{index .Config.Labels "io.authcenter.schema-version"}}' "$TARGET_ID")" = 1 ] || die "镜像数据库版本不兼容。"
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
if [ "$ACTION" = recover ]; then recover || die "恢复失败，请检查容器日志。"; exit 0; fi
[ ! -f "$STATE/pending" ] || die "存在中断操作，请先执行 recover。"

containers=$(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT" --filter label=com.docker.compose.service=auth)
[ "$(printf '%s\n' "$containers" | awk 'NF {n++} END {print n+0}')" -le 1 ] || die "检测到多实例，请先恢复单实例部署。"
VOLUME="${PROJECT}_auth-data"
if [ -n "$containers" ]; then
  mounted=$(docker inspect --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}' "$containers")
  [ "$mounted" = "$VOLUME" ] || die "数据卷与标准部署不一致，拒绝自动替换。"
  actual_id=$(docker inspect --format '{{.Image}}' "$containers")
  [ -z "$CURRENT" ] || [ "$actual_id" = "$CURRENT_ID" ] || die "运行镜像与发布记录不一致，请先核对手工部署变更。"
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
    case "$old_schema" in 1|'') ;; *) die "原镜像数据库结构版本不兼容，拒绝自动接管。" ;; esac
    load_release
    CURRENT=legacy; CURRENT_ID=$actual_id
    mkdir -p "$STATE/versions/legacy"
    cp "$ROOT/compose.yaml" "$STATE/versions/legacy/compose.yaml"
    write_state "$CURRENT" "$CURRENT_ID" "" ""
    ;;
  update)
    [ -n "$CURRENT" ] || die "尚未安装；已有部署请使用 adopt，新部署使用 install。"
    load_release
    if [ "$TARGET" = "$CURRENT" ]; then
      prune_packages
      echo "已经是版本 ${CURRENT}，无需重复安装。"
      exit 0
    fi
    ;;
  rollback)
    [ -n "$PREVIOUS" ] || die "没有可回退版本。"
    TARGET=$PREVIOUS; TARGET_ID=$PREVIOUS_ID
    docker image inspect "$TARGET_ID" >/dev/null || die "回退镜像不存在，请重新导入对应安装包。"
    ;;
esac

# Journal exists before stopping: any subsequent failure attempts the old image.
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
echo "部署版本 $TARGET …"
start "$TARGET" "$TARGET_ID" || die "版本启动失败，正在恢复原版本。"
write_state "$TARGET" "$TARGET_ID" "$CURRENT" "$CURRENT_ID"
rm -f "$STATE/pending"
read_state
prune_packages
echo "完成：当前 ${CURRENT}，可回退到 ${PREVIOUS:-无}。托管安装包仅保留这两个版本；数据备份单独保留。"
