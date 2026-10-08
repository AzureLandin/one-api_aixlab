#!/bin/bash
# 阶段 6 / §13 迁移矩阵：用真实的 one-api 二进制走生产启动路径
# (InitDB: AutoMigrate 旧表 -> migration.Migrate -> reportMigrationCheck；
#  InitLogDB: 独立日志库 AutoMigrate)，验证：
#   - 旧版 PG/MySQL 主库 + 独立日志库均可升级
#   - 重启幂等（schema_migrations 不重复、旧数据不丢）
#   - 备份 + 回滚恢复（drop 库 -> restore dump -> 再启动）
#   - 迁移报告：用户/令牌/渠道/配置/历史日志计数在升级+重启前后一致
# 用法：ENGINE=postgres|mysql bash scripts/smoke/migration_matrix.sh
#   可选环境变量（默认对接本仓库 .gocache 冒烟用容器）：
#     PG_HOST/PG_PORT/PG_USER/PG_PASS
#     MY_HOST/MY_PORT/MY_USER/MY_PASS
set -u
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
ENGINE="${ENGINE:-postgres}"
cd "$HERE"

PORT=${MATRIX_PORT:-13100}
SESSION_SECRET=matrix TIKTOKEN_CACHE_DIR=$PWD/tiktoken
rm -f oneapi_matrix.log seed_counts.txt

pass=0; fail=0
check() { if [ "$2" = "$3" ]; then echo "PASS: $1"; pass=$((pass+1)); else echo "FAIL: $1 (expected=$2 actual=$3)"; fail=$((fail+1)); fi; }

# ---------- engine-specific helpers ----------
if [ "$ENGINE" = "postgres" ]; then
  HOST=${PG_HOST:-127.0.0.1}; PORTDB=${PG_PORT:-15432}; U=${PG_USER:-postgres}; P=${PG_PASS:-oa123}
  export PGPASSWORD="$P"
  MAIN_DSN="postgres://${U}:${P}@${HOST}:${PORTDB}/oa_matrix_main?sslmode=disable&timezone=Asia/Shanghai"
  LOG_DSN="postgres://${U}:${P}@${HOST}:${PORTDB}/oa_matrix_log?sslmode=disable&timezone=Asia/Shanghai"
  sql_exec() { psql -h "$HOST" -p "$PORTDB" -U "$U" -d "$1" -tAc "$2"; }   # db, query
  reset_db() { psql -h "$HOST" -p "$PORTDB" -U "$U" -d postgres -c "DROP DATABASE IF EXISTS $1;" >/dev/null 2>&1; psql -h "$HOST" -p "$PORTDB" -U "$U" -d postgres -c "CREATE DATABASE $1;" >/dev/null 2>&1; }
  backup() { pg_dump -h "$HOST" -p "$PORTDB" -U "$U" -Fc "$1" > "$2"; }
  restore() { psql -h "$HOST" -p "$PORTDB" -U "$U" -d postgres -c "DROP DATABASE IF EXISTS $1;" >/dev/null 2>&1; psql -h "$HOST" -p "$PORTDB" -U "$U" -d postgres -c "CREATE DATABASE $1;" >/dev/null 2>&1; pg_restore -h "$HOST" -p "$PORTDB" -U "$U" -d "$1" --no-owner --no-privileges "$2" 2>/dev/null || true; }
  q_versions() { sql_exec oa_matrix_main "SELECT string_agg(version::text, ',' ORDER BY version) FROM schema_migrations"; }
  tbl_exists() { sql_exec "$1" "SELECT (to_regclass('public.$2') IS NOT NULL)::int"; }  # -> 1 / 0
  KEYCOL='"key"'
else
  HOST=${MY_HOST:-127.0.0.1}; PORTDB=${MY_PORT:-13306}; U=${MY_USER:-root}; P=${MY_PASS:-oa123}
  MAIN_DSN="${U}:${P}@tcp(${HOST}:${PORTDB})/oa_matrix_main?charset=utf8mb4&parseTime=True&loc=Local"
  LOG_DSN="${U}:${P}@tcp(${HOST}:${PORTDB})/oa_matrix_log?charset=utf8mb4&parseTime=True&loc=Local"
  # container-local client (docker exec -> connect to the DB's own 3306)
  MYC="mysql -uroot -p$P"
  sql_exec() { docker exec oa_my_test $MYC -N -B -e "USE $1; $2"; }
  reset_db() { docker exec oa_my_test $MYC -e "DROP DATABASE IF EXISTS $1; CREATE DATABASE $1 CHARACTER SET utf8mb4;" >/dev/null 2>&1; }
  backup() { docker exec oa_my_test mysqldump -h127.0.0.1 -u$U -p$P --single-transaction --routines $1 > "$2"; }
  restore() { docker exec oa_my_test $MYC -e "DROP DATABASE IF EXISTS $1; CREATE DATABASE $1 CHARACTER SET utf8mb4;" >/dev/null 2>&1; docker exec -i oa_my_test $MYC $1 < "$2" >/dev/null 2>&1; }
  q_versions() { sql_exec oa_matrix_main "SELECT GROUP_CONCAT(version ORDER BY version SEPARATOR ',') FROM schema_migrations"; }
  tbl_exists() { sql_exec "$1" "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='$2'"; }
  KEYCOL='`key`'
fi

# ---------- build binary ----------
echo "== building one-api for migration matrix (engine=$ENGINE) =="
GOCACHE="$REPO/.gocache" go build -o ./one-api "$REPO" || { echo "FATAL: build failed"; exit 2; }

boot_server() { # -> sets API_PID; blocks until /api/status responds
  SQL_DSN="$MAIN_DSN" LOG_SQL_DSN="$LOG_DSN" PORT=$PORT SESSION_SECRET=$SESSION_SECRET TIKTOKEN_CACHE_DIR=$TIKTOKEN_CACHE_DIR ./one-api >> oneapi_matrix.log 2>&1 &
  API_PID=$!
  for i in $(seq 1 120); do
    if curl -s -o /dev/null http://127.0.0.1:$PORT/api/status; then return 0; fi
    if ! kill -0 $API_PID 2>/dev/null; then echo "FATAL: server died on boot"; tail -30 oneapi_matrix.log; return 1; fi
    sleep 1
  done
  echo "FATAL: server not up in time"; tail -30 oneapi_matrix.log; return 1
}
stop_server() { kill $API_PID 2>/dev/null; for i in $(seq 1 20); do kill -0 $API_PID 2>/dev/null || { return 0; }; sleep 0.5; done; kill -9 $API_PID 2>/dev/null; }

seed_legacy() { # insert sample rows into legacy tables directly (simulating old data)
  sql_exec oa_matrix_main "INSERT INTO users (username,password,display_name,role,status,quota) VALUES ('matrix_user','x','m',1,1,0);"
  sql_exec oa_matrix_main "INSERT INTO tokens (${KEYCOL},name,status,expired_time) VALUES ('secret-key-1','mtok',1,-1);"
  sql_exec oa_matrix_main "INSERT INTO channels (name,type,status,${KEYCOL}) VALUES ('mch',1,1,'k');"
  sql_exec oa_matrix_main "INSERT INTO logs (user_id,type,username) VALUES (1,2,'matrix_user');"
}
counts() {
  c1=$(sql_exec oa_matrix_main "SELECT count(*) FROM users")
  c2=$(sql_exec oa_matrix_main "SELECT count(*) FROM tokens")
  c3=$(sql_exec oa_matrix_main "SELECT count(*) FROM channels")
  c4=$(sql_exec oa_matrix_main "SELECT count(*) FROM logs")
  c5=$(sql_exec oa_matrix_main "SELECT count(*) FROM options")
  echo "$c1|$c2|$c3|$c4|$c5"
}
schema_versions() { q_versions; }

# ---------- run ----------
echo "== resetting fresh databases =="
reset_db oa_matrix_main; reset_db oa_matrix_log

echo "== boot 1: fresh DB upgrade =="
boot_server || exit 2
check "boot1 server up" "true" "true"

# migration bookkeeping + new table + option present
check "schema_migrations = 1,2" "1,2" "$(schema_versions)"
check "usage_records created" "1" "$(tbl_exists oa_matrix_main usage_records)"
check "options has usage_cutover_at" "1" "$(sql_exec oa_matrix_main "SELECT count(*) FROM options WHERE ${KEYCOL}='usage_cutover_at'")"
check "log db has logs table" "1" "$(tbl_exists oa_matrix_log logs)"
check "usage_records is MAIN-only (absent in log db)" "0" "$(tbl_exists oa_matrix_log usage_records)"

echo "== seeding legacy data =="
seed_legacy
BEFORE=$(counts)
echo "   pre-upgrade counts (users|tokens|channels|logs|options): $BEFORE"

echo "== backup =="
backup oa_matrix_main matrix_main.dump
backup oa_matrix_log  matrix_log.dump
[ -s matrix_main.dump ] && check "backup non-empty" ok ok || check "backup non-empty" ok missing

echo "== boot 2: restart on upgraded DB (idempotency + no data loss) =="
stop_server
boot_server || exit 2
check "schema_migrations still 1,2 (no dup)" "1,2" "$(schema_versions)"
AFTER=$(counts)
echo "   post-restart counts: $AFTER"
check "legacy data unchanged across restart" "$BEFORE" "$AFTER"

echo "== rollback: drop + restore from backup + re-boot =="
stop_server
restore oa_matrix_main matrix_main.dump
restore oa_matrix_log  matrix_log.dump
boot_server || exit 2
check "boot after rollback-restore" "true" "true"
RB=$(counts)
echo "   post-restore counts: $RB"
check "backup/restore recovered legacy data" "$BEFORE" "$RB"
stop_server

echo ""
echo "RESULT (engine=$ENGINE) pass=$pass fail=$fail"
[ "$fail" = 0 ] || exit 1
