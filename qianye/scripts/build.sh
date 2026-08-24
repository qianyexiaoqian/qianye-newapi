#!/bin/sh
# 二开构建包装脚本(POSIX sh 版,Windows 上用同目录的 build.ps1)。
#
# 干的事只有一件:算出要注入的版本号,拼成 ldflags,再调 go build。
#
#   build 二开当前构建的提交  git describe --tags --always --dirty
#                             (只有构建那刻的 git 知道,所以只能注入)
#   core  内核版本            VERSION 文件,为空时取 baseline.txt 的 upstream_tag
#                             **逐字**,不加任何后缀 —— 它必须与上游同义
#
# 二开版本号(qy_version)与「同步到上游哪一版」(upstream_describe)**都不由
# 本脚本注入**:它们声明在 qianye/version/baseline.txt 里,由 Go 侧 go:embed
# 编进二进制。原因分两半:
#   - upstream_describe:旧做法(`git describe --tags --abbrev=0`)量的是 tag
#     可达性,而本 fork 靠逐提交挑拣同步,挑拣不产生祖先关系 —— 树里已经是
#     rc.25 了,describe 还在报 rc.24,不报错。
#   - qy_version:它是人在发版时拍板的,没有哪条 git 命令算得出「该进 MINOR
#     还是 PATCH」。
#
# 【不要再把两个版本号合成一个】曾经这里拼过 `<tag>+qy.<轮次>` 注入
# common.Version。那让「系统维护 → 当前版本」既不是上游版本也不是我们的版本,
# 并且让上游那颗检查更新按钮(它拿这个值跟 release 的 tag_name 做相等比较)
# 永远报「有新版本」。内核版本必须逐字等于上游 tag。
#
# 【必须踩准的坑】-X 的符号路径必须是**完整模块路径**。写成
# `new-api/qianye/version.Build` 这类短路径时,Go 链接器会静默丢弃这条 -X:
# 不报错、不告警、构建成功,只是版本永远是默认值。仓库里
# .github/workflows/release.yml 与 electron-build.yml 的若干处就是这个形态
# (上游既有缺陷),模板要照抄的是 Dockerfile 里 common.Version 那一行。
#
# 用法:
#   sh qianye/scripts/build.sh                 # 构建到仓库根的 new-api[.exe]
#   sh qianye/scripts/build.sh -o /tmp/new-api # 指定产物路径
#   sh qianye/scripts/build.sh --print-only    # 只打印版本与 ldflags,不构建
#   sh qianye/scripts/build.sh --print-core    # 只打印 core 版本号(Dockerfile 用)
#
# 环境变量覆盖(CI 里 .git 常常不可用,这是唯一的注入口):
#   QY_BUILD_VERSION / QY_CORE_VERSION / GO
set -eu

OUTPUT=''
PRINT_ONLY=0
PRINT_CORE=0
while [ $# -gt 0 ]; do
	case "$1" in
	-o | --output)
		shift
		[ $# -gt 0 ] || { echo "build.sh: -o 缺少参数" >&2; exit 2; }
		OUTPUT="$1"
		;;
	--print-only) PRINT_ONLY=1 ;;
	--print-core) PRINT_CORE=1 ;;
	-h | --help)
		sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
		exit 0
		;;
	*)
		echo "build.sh: 未知参数 $1" >&2
		exit 2
		;;
	esac
	shift
done

SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
ROOT=$(CDPATH='' cd -- "$SCRIPT_DIR/../.." && pwd)

# go 不一定在 PATH 上(本机装在 C:\Program Files\Go\bin)。
GO_BIN="${GO:-}"
if [ -z "$GO_BIN" ]; then
	if command -v go >/dev/null 2>&1; then
		GO_BIN=go
	elif [ -x '/c/Program Files/Go/bin/go.exe' ]; then
		GO_BIN='/c/Program Files/Go/bin/go.exe'
	else
		echo 'build.sh: 找不到 go,请把它加进 PATH 或用 GO=/path/to/go 指定' >&2
		exit 1
	fi
fi

# 版本值里出现空白会把 ldflags 拆成两个参数,链接器随后报"unknown flag"。
# git describe / tag 名不可能带空白,但 VERSION 文件和环境变量可能带上
# CR 或尾随换行,所以一律先擦干净。
scrub() { printf '%s' "$1" | tr -d ' \t\r\n'; }

# .git 不可用(容器构建、tar 包分发)时不要崩:退化成空值,
# 最终由 qianye/version 归一成 "unknown"。
git_ok=0
if command -v git >/dev/null 2>&1 &&
	git -C "$ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
	git_ok=1
fi

BUILD_VERSION=$(scrub "${QY_BUILD_VERSION:-}")
if [ -z "$BUILD_VERSION" ] && [ "$git_ok" = 1 ]; then
	# --always:一个 tag 都够不着时回落到裸 commit,总比空着强。
	# --dirty:带未提交改动的产物必须自曝,否则线上排障会指着一个
	# 对不上号的提交找问题。
	BUILD_VERSION=$(scrub "$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || true)")
fi

BASELINE_FILE="$ROOT/qianye/version/baseline.txt"

# baseline_value 从声明文件里取一个键。口径必须与 Go 侧 parseBaseline
# (qianye/version/version.go)和 build.ps1 完全一致,否则同一份声明能编出两个
# 版本号:**精确键名**(前面带 `# ` 的注释行因此不匹配)、**同名键取最后一次**。
baseline_value() {
	[ -f "$BASELINE_FILE" ] || return 0
	scrub "$(sed -n "s/^[[:space:]]*$1[[:space:]]*=//p" "$BASELINE_FILE" | tail -n 1)"
}

CORE_VERSION=$(scrub "${QY_CORE_VERSION:-}")
if [ -z "$CORE_VERSION" ] && [ -f "$ROOT/VERSION" ]; then
	# 仓库里的 VERSION 是 0 字节空文件,上游 CI 构建时才写入 —— 空是常态。
	CORE_VERSION=$(scrub "$(cat "$ROOT/VERSION")")
fi
if [ -z "$CORE_VERSION" ]; then
	# 声明里的上游 tag,**逐字**,不加后缀 —— 加了就不再是「和上游一样」,
	# 而上游那颗检查更新按钮拿它跟 release 的 tag_name 做的是相等比较。
	CORE_VERSION=$(baseline_value upstream_tag)
fi

if [ "$PRINT_CORE" = 1 ]; then
	printf '%s\n' "$CORE_VERSION"
	exit 0
fi

QY_PKG='github.com/QuantumNous/new-api/qianye/version'
LDFLAGS='-s -w'
[ -n "$BUILD_VERSION" ] && LDFLAGS="$LDFLAGS -X $QY_PKG.Build=$BUILD_VERSION"
# 不再注入 Upstream:它已经是 baseline.txt 里的声明,由 go:embed 编进二进制。
# 空值不注入:注入空串会把上游自己的默认值 "v0.0.0" 覆盖成一片空白,
# 那比留着默认值更难看懂。
[ -n "$CORE_VERSION" ] && LDFLAGS="$LDFLAGS -X github.com/QuantumNous/new-api/common.Version=$CORE_VERSION"

if [ -z "$OUTPUT" ]; then
	OUTPUT="$ROOT/new-api$("$GO_BIN" env GOEXE)"
fi

printf 'core     (内核版本) : %s\n' "${CORE_VERSION:-<未注入,保留上游默认值 v0.0.0>}"
printf 'fork     (二开版本) : %s(声明在 baseline.txt,不经 ldflags)\n' \
	"$(baseline_value qy_version)"
printf 'upstream (同步基线) : %s(声明在 baseline.txt,不经 ldflags)\n' \
	"$(baseline_value upstream_describe)"
printf 'build    (构建提交) : %s\n' "${BUILD_VERSION:-<未注入,运行时报 unknown>}"
printf 'ldflags            : %s\n' "$LDFLAGS"
printf 'output             : %s\n' "$OUTPUT"

[ "$PRINT_ONLY" = 1 ] && exit 0

cd "$ROOT"
exec "$GO_BIN" build -ldflags "$LDFLAGS" -o "$OUTPUT" .
