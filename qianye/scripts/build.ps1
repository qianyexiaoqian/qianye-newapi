<#
.SYNOPSIS
二开构建包装脚本(Windows PowerShell 版,POSIX 环境用同目录的 build.sh)。

.DESCRIPTION
干的事只有一件:算出三个版本号,拼成 ldflags,再调 go build。

  build    二开当前版本      git describe --tags --always --dirty
  upstream 同步到的上游 tag  git describe --tags --abbrev=0
  core     上游自己的版本号   VERSION 文件,为空时回落到 upstream

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
QY_BUILD_VERSION / QY_UPSTREAM_VERSION / QY_CORE_VERSION / GO

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

$upstreamVersion = Convert-QyVersionToken $env:QY_UPSTREAM_VERSION
if ([string]::IsNullOrEmpty($upstreamVersion)) {
    # 前提:Fork 自己不打 tag,因此 HEAD 能够到的最近 tag 必然来自上游,
    # 也就是"最近一次同步到哪个上游版本"。哪天二开开始自己打 tag,
    # 这一行要改成沿 upstream/main 求交集。
    $upstreamVersion = Invoke-QyGit @('describe', '--tags', '--abbrev=0')
}

$coreVersion = Convert-QyVersionToken $env:QY_CORE_VERSION
$versionFile = Join-Path $root 'VERSION'
if ([string]::IsNullOrEmpty($coreVersion) -and (Test-Path $versionFile)) {
    # 仓库里的 VERSION 是 0 字节空文件,CI 构建时才写入 —— 空是常态。
    $coreVersion = Convert-QyVersionToken (Get-Content $versionFile -Raw -ErrorAction SilentlyContinue)
}
if ([string]::IsNullOrEmpty($coreVersion)) {
    $coreVersion = $upstreamVersion
}

$qyPkg = 'github.com/QuantumNous/new-api/qianye/version'
$parts = @('-s', '-w')
if (-not [string]::IsNullOrEmpty($buildVersion)) {
    $parts += "-X $qyPkg.Build=$buildVersion"
}
if (-not [string]::IsNullOrEmpty($upstreamVersion)) {
    $parts += "-X $qyPkg.Upstream=$upstreamVersion"
}
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
if ([string]::IsNullOrEmpty($upstreamVersion)) { $shownUpstream = $unset } else { $shownUpstream = $upstreamVersion }
if ([string]::IsNullOrEmpty($coreVersion)) { $shownCore = '<未注入,保留上游默认值>' } else { $shownCore = $coreVersion }

"build    (二开当前) : $shownBuild"
"upstream (同步上游) : $shownUpstream"
"core     (上游内核) : $shownCore"
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
