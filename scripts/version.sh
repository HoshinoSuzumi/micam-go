#!/bin/sh
# 打印这次构建的版本号。
#
# 版本只有一个来源：git tag。发布时 CI 把 tag 去掉 v 通过 MICAM_VERSION 传进来；
# 本地构建时退回到 git describe，所以 `make build` 出来的二进制永远对得上当时的
# 提交，而不是某个忘了改的字面量。
#
#   MICAM_VERSION=0.6.2        → 0.6.2
#   在 v0.6.2 上               → 0.6.2
#   在 v0.6.2 之后 3 个提交    → 0.6.2-3-gabc1234（脏工作区再加 -dirty）
#   没有 tag 的仓库             → dev
set -eu

if [ -n "${MICAM_VERSION:-}" ]; then
    printf '%s\n' "${MICAM_VERSION#v}"
    exit 0
fi

if command -v git >/dev/null 2>&1; then
    if described=$(git describe --tags --always --dirty 2>/dev/null); then
        printf '%s\n' "${described#v}"
        exit 0
    fi
fi

printf 'dev\n'
