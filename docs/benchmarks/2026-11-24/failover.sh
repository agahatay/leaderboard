#!/usr/bin/env bash
# usage: failover.sh <nginx conf> <board> <out dir>   (run from the repository root)
# The script used for the two runs in the README; only the LOADGEN line differs.
set -euo pipefail
CONF=$1 BOARD=$2 OUT=$3
LOADGEN=${LOADGEN:-./loadgen.exe}   # the measured runs used a build of cmd/loadgen in a temp folder
DC="docker compose -f docker-compose.yml -f compose.lb.yml"
export NGINX_CONF=$CONF
mkdir -p "$OUT"

$DC up -d --scale api=3 --wait >/dev/null 2>&1
$DC up -d --no-deps --force-recreate --wait nginx >/dev/null 2>&1
{ for i in 1 2 3; do echo "api-$i $(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' leaderboard-api-$i)"; done
  $DC exec -T nginx nginx -T 2>/dev/null | grep -E "next_upstream|access_log"; } > "$OUT/setup.txt"
set -a; . ./.env; set +a
$DC exec -T redis redis-cli FLUSHALL >/dev/null
"$LOADGEN" -c 50 -d 5s -board warmup >/dev/null

echo "start $(date -Iseconds)" > "$OUT/timeline.txt"
"$LOADGEN" -c 50 -d 30s -players 0 -board "$BOARD" > "$OUT/loadgen.txt" &
pid=$!
sleep 10
echo "docker stop leaderboard-api-2 at $(date -Iseconds)" >> "$OUT/timeline.txt"
docker stop leaderboard-api-2 >/dev/null
echo "stopped at $(date -Iseconds)" >> "$OUT/timeline.txt"
wait $pid
echo "loadgen done $(date -Iseconds)" >> "$OUT/timeline.txt"

docker logs leaderboard-api-2 2>&1 | grep -vE '"msg":"http request"' | tail -n 2 > "$OUT/api-2.txt"
docker logs leaderboard-nginx-1 > "$OUT/nginx.log" 2>&1
echo "ZCARD lb:$BOARD = $($DC exec -T redis redis-cli ZCARD lb:$BOARD)" > "$OUT/zcard.txt"
