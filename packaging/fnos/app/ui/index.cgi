#!/bin/bash
# 应用界面入口：静态文件走 CGI，/api/* 交给 micam ctl。
#
# 走飞牛网关（/cgi/ThirdParty/micam/…）意味着界面和 API 都继承了网关的登录态，
# 不需要再开一个端口，也不用自己做鉴权。
BASE_PATH="${TRIM_APPDEST:-/var/apps/micam/target}/www"
BIN="${TRIM_APPDEST:-/var/apps/micam/target}/bin/micam"
URI_NO_QUERY="${REQUEST_URI%%\?*}"
REL_PATH="/"

case "$URI_NO_QUERY" in
*index.cgi*)
    REL_PATH="${URI_NO_QUERY#*index.cgi}"
    ;;
esac

if [ -z "$REL_PATH" ] || [ "$REL_PATH" = "/" ]; then
    REL_PATH="/index.html"
fi

# 管理接口：交给二进制，它读 PATH_INFO 决定路由。
case "$REL_PATH" in
/api/*)
    PATH_INFO="$REL_PATH"
    export PATH_INFO
    exec "$BIN" ctl
    ;;
esac

TARGET_FILE="${BASE_PATH}${REL_PATH}"

if echo "$TARGET_FILE" | grep -q '\.\.'; then
    echo "Status: 400 Bad Request"
    echo "Content-Type: text/plain; charset=utf-8"
    echo ""
    echo "Bad Request"
    exit 0
fi

if [ ! -f "$TARGET_FILE" ]; then
    echo "Status: 404 Not Found"
    echo "Content-Type: text/plain; charset=utf-8"
    echo ""
    echo "404 Not Found"
    exit 0
fi

case "${TARGET_FILE##*.}" in
html | htm) mime="text/html; charset=utf-8" ;;
css) mime="text/css; charset=utf-8" ;;
js | mjs) mime="application/javascript; charset=utf-8" ;;
json) mime="application/json; charset=utf-8" ;;
png) mime="image/png" ;;
jpg | jpeg) mime="image/jpeg" ;;
svg) mime="image/svg+xml" ;;
woff2) mime="font/woff2" ;;
*) mime="application/octet-stream" ;;
esac

echo "Content-Type: $mime"
echo ""
cat "$TARGET_FILE"
