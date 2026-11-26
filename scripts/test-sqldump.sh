#!/usr/bin/env bash
# sqldump 真实服务器矩阵测试（docs/spec.md §11.5）。
#
# 用 Docker 起 MySQL 5.7 / 8.0 / 8.4 / 9.x 与 MariaDB 10.11 / 11.x，等就绪后设置 LEMI_TEST_MYSQL 与
# LEMI_TEST_MYSQL_DOCKER 跑 go test，结束（含中断）时删掉本次建的容器、网络、卷，以及本次新拉的镜像
# （本机原有的镜像不动）。
#
# 用法：scripts/test-sqldump.sh [go test 额外参数，例如 -run TestMySQLMatrix -short]
# 环境变量：
#   LEMI_DOCKER_PREFIX     镜像前缀，默认 docker.1panel.live/library/（本机直连 Docker Hub 会超时；CI 可设为空）
#   LEMI_SQLDUMP_SERVERS   只跑其中几台，逗号分隔，例如 mysql84,mariadb114；默认全部
#   LEMI_MYSQL9_TAG        MySQL 9.x 的镜像标签，默认 9.7（目前最新；镜像站的 9 标签拉取会卡住）
#   LEMI_MARIADB11_TAG     MariaDB 11.x 的镜像标签，默认 11.4
#   LEMI_KEEP_IMAGES=1     不删本次新拉的镜像（反复跑时省得重拉）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PREFIX="${LEMI_DOCKER_PREFIX-docker.1panel.live/library/}"
MYSQL9_TAG="${LEMI_MYSQL9_TAG:-9.7}"
MARIADB11_TAG="${LEMI_MARIADB11_TAG:-11.4}"
RUN_ID="$$"
NETWORK="lemi-sqldump-net-${RUN_ID}"
# 随机密码：只用于本机临时容器，不进仓库。
PASSWORD="lemi${RANDOM}${RANDOM}${RANDOM}"

HOST_ARCH="$(uname -m)"
AMD64_PLATFORM=""
if [ "${HOST_ARCH}" = "arm64" ] || [ "${HOST_ARCH}" = "aarch64" ]; then
  AMD64_PLATFORM="linux/amd64" # mysql:5.7 只有 amd64 镜像
fi

# 名字|镜像|主机端口|平台|类型
ALL_SERVERS=(
  "mysql57|mysql:5.7|33057|${AMD64_PLATFORM}|mysql"
  "mysql80|mysql:8.0|33080||mysql"
  "mysql84|mysql:8.4|33084||mysql"
  "mysql9|mysql:${MYSQL9_TAG}|33090||mysql"
  "mariadb1011|mariadb:10.11|33111||mariadb"
  "mariadb11|mariadb:${MARIADB11_TAG}|33112||mariadb"
)

SERVERS=()
for s in "${ALL_SERVERS[@]}"; do
  name="${s%%|*}"
  if [ -z "${LEMI_SQLDUMP_SERVERS:-}" ] || [[ ",${LEMI_SQLDUMP_SERVERS}," == *",${name},"* ]]; then
    SERVERS+=("$s")
  fi
done
if [ "${#SERVERS[@]}" -eq 0 ]; then
  echo "LEMI_SQLDUMP_SERVERS 没有匹配任何服务器" >&2
  exit 2
fi

PULLED_IMAGES=()
CONTAINERS=()

cleanup() {
  local code=$?
  set +e
  echo "== 清理"
  for c in "${CONTAINERS[@]+"${CONTAINERS[@]}"}"; do
    vols="$(docker inspect -f '{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}' "$c" 2>/dev/null)"
    docker rm -f -v "$c" >/dev/null 2>&1
    for v in $vols; do docker volume rm -f "$v" >/dev/null 2>&1; done
    echo "已删除容器 ${c}"
  done
  docker network rm "${NETWORK}" >/dev/null 2>&1 && echo "已删除网络 ${NETWORK}"
  if [ "${LEMI_KEEP_IMAGES:-}" != "1" ]; then
    for img in "${PULLED_IMAGES[@]+"${PULLED_IMAGES[@]}"}"; do
      docker rmi "$img" >/dev/null 2>&1 && echo "已删除本次拉取的镜像 ${img}"
    done
  fi
  exit $code
}
trap cleanup EXIT INT TERM

docker network create "${NETWORK}" >/dev/null

for s in "${SERVERS[@]}"; do
  IFS='|' read -r name image port platform kind <<<"$s"
  ref="${PREFIX}${image}"
  if ! docker image inspect "$ref" >/dev/null 2>&1; then
    echo "== 拉取 ${ref}"
    if [ -n "$platform" ]; then
      docker pull --platform "$platform" "$ref"
    else
      docker pull "$ref"
    fi
    PULLED_IMAGES+=("$ref")
  fi
  cname="lemi-sqldump-${name}-${RUN_ID}"
  args=(run -d --name "$cname" --network "$NETWORK" -p "127.0.0.1:${port}:3306")
  if [ -n "$platform" ]; then args+=(--platform "$platform"); fi
  if [ "$kind" = "mariadb" ]; then
    args+=(-e "MARIADB_ROOT_PASSWORD=${PASSWORD}")
  else
    args+=(-e "MYSQL_ROOT_PASSWORD=${PASSWORD}")
  fi
  args+=("$ref" --event-scheduler=OFF --max-allowed-packet=256M)
  echo "== 启动 ${name}（${ref}）"
  docker "${args[@]}" >/dev/null
  CONTAINERS+=("$cname")
done

DSNS=()
DOCKERS=()
for s in "${SERVERS[@]}"; do
  IFS='|' read -r name image port platform kind <<<"$s"
  cname="lemi-sqldump-${name}-${RUN_ID}"
  client="mysql"
  if [ "$kind" = "mariadb" ]; then client="mariadb"; fi
  printf "== 等待 %s 就绪" "$name"
  ok=""
  for _ in $(seq 1 180); do
    # 走 TCP：镜像初始化阶段的临时服务器不监听网络，能连上 TCP 才是真正就绪。
    if docker exec "$cname" "$client" --protocol=TCP -h127.0.0.1 -P3306 -uroot "-p${PASSWORD}" -e "SELECT 1" >/dev/null 2>&1; then
      ok=1
      break
    fi
    printf "."
    sleep 2
  done
  echo
  if [ -z "$ok" ]; then
    echo "${name} 没有就绪，最后的日志：" >&2
    docker logs --tail 50 "$cname" >&2
    exit 1
  fi
  DSNS+=("${name}=root:${PASSWORD}@tcp(127.0.0.1:${port})/")
  DOCKERS+=("${name}=${cname}")
done

LEMI_TEST_MYSQL="$(IFS=,; echo "${DSNS[*]}")"
LEMI_TEST_MYSQL_DOCKER="$(IFS=,; echo "${DOCKERS[*]}")"
export LEMI_TEST_MYSQL LEMI_TEST_MYSQL_DOCKER

echo "== go test"
cd "${ROOT}/lemi-service"
go test ./internal/sqlsplit/ ./internal/sqldump/ -count=1 -timeout 90m "$@"
