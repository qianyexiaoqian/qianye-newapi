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
 * 前端测试闸门。用 `bun run test` 跑，不要直接 `bun test src`。
 *
 * # 为什么不能直接 `bun test src`
 *
 * 测试文件写的是 `node:test` + `node:assert`，而本仓的运行器是 bun。bun 的
 * `node:test` 垫片有一个未实现分支：`describe() inside another test()`
 * (https://github.com/oven-sh/bun/issues/5090)。一旦某个文件里有异步测试
 * (React `act` 那一批) 还没跑完，后续文件的顶层 `describe` 就会被当成
 * "嵌在别人的 test 里"而直接抛错。
 *
 * 后果不是"多几条噪音"，是**闸门失效**：整个 src 一起跑，实测
 * 316 pass / 125 fail / 122 errors / 441 tests；而逐目录跑是
 * 1363 pass / 8 fail / 1371 tests。也就是说批量模式静默吞掉了一千多条测试，
 * 还凭空造出一百多条假失败 —— 真回归与垫片噪音无法区分，跑了等于没跑。
 *
 * 所以这里按目录分进程跑，每个目录一个干净的 bun 进程。
 *
 * # 已知失败必须显式登记
 *
 * KNOWN_FAILURES 里的条目是**上游自带**的失败，不是本仓引入的。登记成
 * 精确条数而不是"忽略这个目录"：多一条少一条都会让闸门变红，于是
 * "上游那几条"和"我刚写坏的那条"仍然分得开。修好之后把条数改成 0 或删掉
 * 这一行，别把它当成长期豁免。
 */
import { spawnSync } from 'node:child_process'
import { readdirSync, statSync } from 'node:fs'
import path from 'node:path'

/** 目录 → 允许的失败条数。写明来源，否则下一个人不知道能不能删。 */
const KNOWN_FAILURES = {
  // 上游 API Key 分组单元格的 Auto 动效测试，与本仓改动无关。
  'src/features/keys': 8,
}

/** 收集所有含测试文件的"跑测单元"：src/features/<name> 与其余 src/<name>。 */
function collectSuites(root) {
  const suites = new Set()
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      const full = path.join(dir, name)
      if (statSync(full).isDirectory()) {
        if (name === 'node_modules') continue
        walk(full)
        continue
      }
      if (!/\.(test|spec)\.[cm]?[jt]sx?$/.test(name)) continue
      const rel = path.relative(root, full).split(path.sep)
      // src/features/qy/... → src/features/qy;src/lib/... → src/lib
      const depth = rel[0] === 'features' ? 2 : 1
      suites.add(['src', ...rel.slice(0, depth)].join('/'))
    }
  }
  walk(path.join(root))
  return [...suites].sort()
}

const root = path.join(import.meta.dirname, '..', 'src')
const suites = collectSuites(root)

let totalPass = 0
let totalFail = 0
let unexpected = 0

for (const suite of suites) {
  const run = spawnSync('bun', ['test', suite], {
    encoding: 'utf8',
    shell: process.platform === 'win32',
  })
  const out = `${run.stdout ?? ''}${run.stderr ?? ''}`
  const pass = Number(out.match(/(\d+) pass/)?.[1] ?? 0)
  const fail = Number(out.match(/(\d+) fail/)?.[1] ?? 0)
  const allowed = KNOWN_FAILURES[suite] ?? 0
  totalPass += pass
  totalFail += fail

  if (fail === allowed) {
    const note = allowed > 0 ? ` (${allowed} known upstream)` : ''
    console.log(`  ok   ${suite}  ${pass} pass${note}`)
    continue
  }
  unexpected++
  console.log(
    `  FAIL ${suite}  ${pass} pass, ${fail} fail (expected ${allowed})`
  )
  // 只在对不上的时候打全文：全绿时刷屏会让真正的失败被卷走。
  console.log(out.replace(/^/gm, '       '))
}

console.log(
  `\n${totalPass} pass, ${totalFail} fail across ${suites.length} suites`
)
if (unexpected > 0) {
  console.log(`${unexpected} suite(s) did not match the recorded baseline`)
  process.exit(1)
}
