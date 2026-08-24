<#
.SYNOPSIS
二开构建包装脚本(Windows PowerShell 版,POSIX 环境用同目录的 build.sh)。

.DESCRIPTION
干的事只有一件:算出要注入的版本号,拼成 ldflags,再调 go build。

  build 二开当前构建的提交  git describe --tags --always --dirty
                            (只有构建那刻的 git 知道,所以只能注入)
  core  内核版本            VERSION 文件,为空时取 baseline.txt 的 upstream_tag
                            **逐字**,不加任何后缀 —— 它必须与上游同义

二开版本号(qy_version)与「同步到上游哪一版」(upstream_describe)**都不由
本脚本注入**:它们声明在 qianye/version/baseline.txt 里,由 Go 侧 go:embed
编进二进制。原因分两半:upstream_describe 的旧做法
(`git describe --tags --abbrev=0`)量的是 tag 可达性,而本 fork 靠逐提交挑拣
同步,挑拣不产生祖先关系 —— 树里已经是 rc.25 了,describe 还在报 rc.24,不报错;
qy_version 则是人在发版时拍板的,没有哪条 git 命令算得出「该进 MINOR 还是 PATCH」。

【不要再把两个版本号合成一个】曾经这里拼过 `<tag>+qy.<轮次>` 注入
common.Version。那让「系统维护 → 当前版本」既不是上游版本也不是我们的版本,
并且让上游那颗检查更新按钮(它拿这个值跟 release 的 tag_name 做相等比较)
永远报「有新版本」。内核版本必须逐字等于上游 tag。

【必须踩准的坑】-X 的符号路径必须是**完整模块路径**。写成
`new-api/qianye/version.Build` 这类短路径时,Go 链接器会静默丢弃这条 -X:
不报错、不告警、构建成功,只是版本永远是默认值。仓库里
.github/workflows/release.yml 与 electron-build.yml 的若干处就是这个形态
(上游既有缺陷),模板要照抄的是 Dockerfile 里 common.Version 那一行。

.PARAMETER Output
产物路径。默认是仓库根的 new-api.exe。

.PARAMETER PrintOnly
只打印版本与 ldflags,不真的构建。

.EXAMPLE
powershell -File qianye\scripts\build.ps1

.NOTES
环境变量覆盖(CI 里 .git 常常不可用,这是唯一的注入口):
QY_BUILD_VERSION / QY_CORE_VERSION / GO

本文件必须以 **UTF-8 BOM** 保存。Windows PowerShell 5.1 对无 BOM 的脚本按
系统 ANSI 代码页解码,上面这些中文注释会变成乱码,并且乱码字节会撞进
PowerShell 的词法分析 —— 表现是一堆莫名其妙的 "Unexpected token" 解析错误,
而不是"编码不对"。改完这个文件请确认 BOM 还在。
#>
[CmdletBinding()]
param(
    [string]$Output,
    [switch]$PrintOnly
)

$ErrorActionPreference = 'Stop'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path

# go 不一定在 PATH 上(本机装在 C:\Program Files\Go\bin)。
$goBin = $env:GO
if ([string]::IsNullOrWhiteSpace($goBin)) {
    $onPath = Get-Command go -ErrorAction SilentlyContinue
    if ($null -ne $onPath) {
        $goBin = $onPath.Source
    }
    else {
        $fallback = 'C:\Program Files\Go\bin\go.exe'
        if (Test-Path $fallback) {
            $goBin = $fallback
        }
        else {
            throw 'build.ps1: 找不到 go,请把它加进 PATH 或用 $env:GO 指定'
        }
    }
}

# 版本值里出现空白会把 ldflags 拆成两个参数,链接器随后报 "unknown flag"。
# git describe / tag 名不可能带空白,但 VERSION 文件和环境变量可能带上 CR
# 或尾随换行,所以一律先擦干净。
function Convert-QyVersionToken([string]$raw) {
    if ($null -eq $raw) { return '' }
    return ($raw -replace '\s', '')
}

# Get-QyBaselineValue 从 qianye/version/baseline.txt 里取一个键。
#
# 口径必须与 Go 侧 parseBaseline(qianye/version/version.go)和 build.sh 完全一致,
# 否则同一份声明能编出两个版本号:**精确键名**(前面带 `# ` 的注释行因此不匹配)、
# **同名键取最后一次**。
#
# -Encoding UTF8 不能省:Windows PowerShell 5.1 的 Get-Content 默认按系统 ANSI
# 代码页解码,而这个文件带中文注释。键与值虽然都是 ASCII,但让解码口径含糊
# 没有任何好处。
function Get-QyBaselineValue([string]$key) {
    $file = Join-Path $root 'qianye\version\baseline.txt'
    if (-not (Test-Path $file)) { return '' }
    $found = ''
    foreach ($line in (Get-Content $file -Encoding UTF8)) {
        $trimmed = $line.Trim()
        if ($trimmed -eq '') { continue }
        $idx = $trimmed.IndexOf('=')
        if ($idx -lt 0) { continue }
        if ($trimmed.Substring(0, $idx).Trim() -ne $key) { continue }
        $found = $trimmed.Substring($idx + 1)
    }
    return Convert-QyVersionToken $found
}

# .git 不可用(容器构建、tar 包分发)时不要崩:退化成空值,
# 最终由 qianye/version 归一成 "unknown"。
function Invoke-QyGit([string[]]$gitArgs) {
    if ($null -eq (Get-Command git -ErrorAction SilentlyContinue)) { return '' }
    try {
        $out = & git -C $root @gitArgs 2>$null
        if ($LASTEXITCODE -ne 0) { return '' }
        return Convert-QyVersionToken ($out | Select-Object -First 1)
    }
    catch {
        return ''
    }
}

$buildVersion = Convert-QyVersionToken $env:QY_BUILD_VERSION
if ([string]::IsNullOrEmpty($buildVersion)) {
    # --always:一个 tag 都够不着时回落到裸 commit,总比空着强。
    # --dirty:带未提交改动的产物必须自曝,否则线上排障会指着一个
    # 对不上号的提交找问题。
    $buildVersion = Invoke-QyGit @('describe', '--tags', '--always', '--dirty')
}

$coreVersion = Convert-QyVersionToken $env:QY_CORE_VERSION
$versionFile = Join-Path $root 'VERSION'
if ([string]::IsNullOrEmpty($coreVersion) -and (Test-Path $versionFile)) {
    # 仓库里的 VERSION 是 0 字节空文件,上游 CI 构建时才写入 —— 空是常态。
    $coreVersion = Convert-QyVersionToken (Get-Content $versionFile -Raw -ErrorAction SilentlyContinue)
}
if ([string]::IsNullOrEmpty($coreVersion)) {
    # 声明里的上游 tag,**逐字**,不加后缀 —— 加了就不再是「和上游一样」,
    # 而上游那颗检查更新按钮拿它跟 release 的 tag_name 做的是相等比较。
    $coreVersion = Get-QyBaselineValue 'upstream_tag'
}

$qyPkg = 'github.com/QuantumNous/new-api/qianye/version'
$parts = @('-s', '-w')
if (-not [string]::IsNullOrEmpty($buildVersion)) {
    $parts += "-X $qyPkg.Build=$buildVersion"
}
# 不再注入 Upstream:它已经是 baseline.txt 里的声明,由 go:embed 编进二进制。
# 空值不注入:注入空串会把上游自己的默认值 "v0.0.0" 覆盖成一片空白,
# 那比留着默认值更难看懂。
if (-not [string]::IsNullOrEmpty($coreVersion)) {
    $parts += "-X github.com/QuantumNous/new-api/common.Version=$coreVersion"
}
$ldflags = $parts -join ' '

if ([string]::IsNullOrWhiteSpace($Output)) {
    $goExe = (& $goBin env GOEXE)
    $Output = Join-Path $root "new-api$goExe"
}

$unset = '<未注入,运行时报 unknown>'
if ([string]::IsNullOrEmpty($buildVersion)) { $shownBuild = $unset } else { $shownBuild = $buildVersion }
if ([string]::IsNullOrEmpty($coreVersion)) { $shownCore = '<未注入,保留上游默认值 v0.0.0>' } else { $shownCore = $coreVersion }
$shownFork = Get-QyBaselineValue 'qy_version'
$shownUpstream = Get-QyBaselineValue 'upstream_describe'

"core     (内核版本) : $shownCore"
"fork     (二开版本) : $shownFork(声明在 baseline.txt,不经 ldflags)"
"upstream (同步基线) : $shownUpstream(声明在 baseline.txt,不经 ldflags)"
"build    (构建提交) : $shownBuild"
"ldflags            : $ldflags"
"output             : $Output"

if ($PrintOnly) { return }

Push-Location $root
try {
    & $goBin build -ldflags $ldflags -o $Output .
    if ($LASTEXITCODE -ne 0) { throw "build.ps1: go build 失败(退出码 $LASTEXITCODE)" }
}
finally {
    Pop-Location
}
