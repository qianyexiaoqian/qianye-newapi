/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
/**
 * oxfmt 包装器：格式化整棵 web/，但**保护受 AGENTS.md 治理条款约束的版权头**。
 *
 * # 为什么必须先剥离版权头
 *
 * `.oxfmtrc.json` 开了 `sortImports`。oxfmt 把文件顶部的那段 `/* Copyright ... *\/`
 * 当成**第一条 import 的前导注释**：一旦排序把那条 import 挪走，版权头就跟着它一起
 * 挪到文件中间。实测（相对 import 在前、外部包在后）：
 *
 *     /* Copyright (C) 2023-2026 QuantumNous ... *\/     import { z } from 'zod'
 *     import { a } from './a'              ──oxfmt──▶
 *     import { z } from 'zod'                           /* Copyright ... *\/
 *                                                       import { a } from './a'
 *
 * 所以「先摘掉、格式化、再贴回」这一步是必需的，不能简化掉。
 *
 * # 为什么不在原地剥离（也就是为什么不是加一把文件锁）
 *
 * 旧实现直接在工作树里把 1600 个文件的版权头删掉、跑 oxfmt、再写回去，
 * `--check` 还额外快照/还原整棵树。这中间有一段**工作树处于无版权头状态**的窗口，
 * 于是两个进程交错时会这样丢：
 *
 *     A: 剥离(树上无头) ─────────────────────────────▶ 还原(贴回)
 *     B:        快照(拍到的是无头的树) ── oxfmt ── 还原快照(把无头版写回去) ✗
 *
 * 实测造成 446 个文件的版权头整段消失，直接违反 AGENTS.md 的「受保护标识不得删除」。
 *
 * 加一把文件锁能挡住这条交错，但挡不住另外两件同样真实的事：
 *   1. `--check` 仍然会写工作树。一个自称只读的闸门在跑的时候把版权头摘掉，
 *      同时开着的 tsgo / bun test / 编辑器读到的就是无头版；Ctrl-C 或断电停在
 *      那个窗口里，头就永久没了 —— 锁保护不了非它管辖的读者，也保护不了崩溃。
 *   2. 锁本身要处理陈旧锁（进程被 kill -9 后锁文件还在），而判断"锁陈旧了没有"
 *      在跨平台上没有可靠做法。
 *
 * 所以这里换成**镜像**：把整棵树按「已剥离版权头」的样子复制到系统临时目录，
 * 在镜像里跑一次 oxfmt，再把「版权头 + 格式化后的正文」与原文件逐一比对。
 *   - `--check` 一个字节都不写工作树；
 *   - `--write` 只在内容真的变了时写回单个文件，任何时刻工作树里都带着版权头；
 *   - 两个并发实例各自有独立镜像，写回的是同一份幂等结果，不存在"看到半成品"；
 *   - 中途被杀最坏只是留下一个临时目录，工作树不受影响。
 *
 * 代价是一次约 15MB / 1600 文件的复制（本机约 0.4s），换掉的是一整类数据丢失。
 */
import { spawnSync } from 'node:child_process'
import {
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join, relative } from 'node:path'

const mode = process.argv[2]

if (mode !== '--check' && mode !== '--write') {
  console.error(
    'Usage: node scripts/format-with-protected-headers.mjs --check|--write'
  )
  process.exit(2)
}

const root = process.cwd()
const excludedDirs = new Set([
  '.git',
  '.tanstack',
  'build',
  'coverage',
  'dist',
  'node_modules',
])
const headerExtensions = new Set([
  '.cjs',
  '.cts',
  '.js',
  '.jsx',
  '.mjs',
  '.mts',
  '.ts',
  '.tsx',
])
const protectedHeaderPattern =
  /^\/\*\nCopyright \(C\)[\s\S]*?QuantumNous[\s\S]*?\*\/\n+/

function extensionOf(path) {
  const index = path.lastIndexOf('.')
  return index === -1 ? '' : path.slice(index)
}

function walk(dir, files = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.isDirectory()) {
      if (!excludedDirs.has(entry.name)) {
        walk(join(dir, entry.name), files)
      }
      continue
    }

    if (entry.isFile()) {
      files.push(join(dir, entry.name))
    }
  }

  return files
}

/**
 * 把工作树复制进镜像，顺手摘掉受保护的版权头。
 *
 * 返回每个文件的 { original, header }：`original` 是复制那一刻的原始字节，
 * 后面用它判断「格式化后到底变没变」；`header` 为空串表示这个文件没有版权头。
 */
function mirrorTree(files, mirror) {
  const entries = new Map()

  for (const file of files) {
    const rel = relative(root, file)
    const target = join(mirror, rel)
    mkdirSync(dirname(target), { recursive: true })

    const original = readFileSync(file)
    let header = ''
    let body = original

    if (headerExtensions.has(extensionOf(file))) {
      const text = original.toString('utf8')
      const match = text.match(protectedHeaderPattern)
      if (match) {
        header = match[0]
        body = Buffer.from(text.slice(match[0].length), 'utf8')
      }
    }

    writeFileSync(target, body)
    entries.set(rel, { original, header })
  }

  return entries
}

/**
 * 读回镜像里格式化后的正文，把版权头原样贴回去。
 *
 * `replace(/^\n+/, '')` 与旧实现一致：oxfmt 可能在正文开头留下空行，
 * 而 scripts/add-copyright.mjs 要求 `*\/` 后面紧跟代码，中间不许有空行。
 */
function reattachHeader(mirror, rel, header) {
  const formatted = readFileSync(join(mirror, rel))
  if (header === '') {
    return formatted
  }
  const body = formatted.toString('utf8').replace(/^\n+/, '')
  return Buffer.from(header + body, 'utf8')
}

const files = walk(root).filter(
  (file) => statSync(file).size < 10 * 1024 * 1024
)

const mirror = mkdtempSync(join(tmpdir(), 'oxfmt-protected-headers-'))
let exitCode = 0

try {
  const entries = mirrorTree(files, mirror)

  const result = spawnSync(
    'oxfmt',
    ['-c', '.oxfmtrc.json', '--ignore-path', '.gitignore', '--write', '.'],
    {
      cwd: mirror,
      stdio: 'inherit',
    }
  )
  /*
    oxfmt 起不来(ENOENT —— 不经 bun/npm 直接 `node scripts/...` 时
    node_modules/.bin 不在 PATH 里)时，spawnSync 的 status 是 null 而
    result.error 才带着原因，`stdio:'inherit'` 又意味着 Node 一个字节都没写。

    此前这里是 `result.status ?? 1`：null 被折叠成 1，而 error 从头到尾没人读，
    于是脚本可以在 **stdout 与 stderr 都是 0 字节** 的情况下退出 1 —— 与
    「发现格式问题」在退出码上不可区分。--write 模式更糟：它同样 exit 1 且
    一个文件都没格式化，调用方却以为自己刚跑过一次格式化。这个脚本的
    usage 串教的正是 `node scripts/format-with-protected-headers.mjs`。

    所以：起不来就把原因原样打出来，并用一个**与格式问题不同**的退出码。
  */
  const spawnFailed = result.error != null || result.status == null
  if (spawnFailed) {
    console.error(
      'Failed to run oxfmt (is node_modules/.bin on PATH? use `bun run format:check`):'
    )
    console.error(String(result.error ?? `exited with signal ${result.signal}`))
    // 2 而不是 1：调用方（人或 CI）必须能把「这个仓库有文件没格式化」
    // 与「格式化器根本没跑起来」分开。exitCode 不走 process.exit，
    // 是为了让下面的 finally 把镜像目录删干净。
    exitCode = 2
  } else {
    exitCode = result.status
  }

  if (exitCode === 0) {
    const changed = []

    for (const [rel, { original, header }] of entries) {
      const next = reattachHeader(mirror, rel, header)
      if (original.equals(next)) {
        continue
      }
      changed.push(rel)
      if (mode === '--write') {
        writeFileSync(join(root, rel), next)
      }
    }

    if (mode === '--check' && changed.length > 0) {
      console.error('Format issues found in protected-header-safe check:')
      for (const file of changed) {
        console.error(file)
      }
      exitCode = 1
    }
  }
} finally {
  rmSync(mirror, { force: true, recursive: true })
}

process.exit(exitCode)
