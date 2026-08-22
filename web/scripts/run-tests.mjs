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
 * # 一个目录里出现 `N errors` 时，逐文件重跑
 *
 * 按目录分进程只把上面那条垫片 bug 缩小到了"一个目录内部"，没有消灭它：
 * `src/features/keys` 这个跑测单元一个进程跑 7 个文件，第一个文件的异步用例
 * 还没跑完，后面**每一个**文件的顶层 describe 都会抛
 * `describe() inside another test()`，整份文件被静默吞掉。实测原有 6 个文件
 * 里 5 个从来没执行过，26 条断言是死的。
 *
 * 而这条 bug 之所以能活这么久，是因为闸门**只解析 pass / fail，从不解析
 * `N errors`，也从不看进程退出码**：bun 把 5 个 error 折进了 `8 fail`，
 * 恰好等于 KNOWN_FAILURES 里登记的 8，于是走 ok 分支，连全文都不打印。
 * 也就是说"整份文件没跑"与"上游自带的失败"在闸门眼里是同一件事。
 * 实测破坏两处共享代码（group-options 的空 desc 回落、combobox 的 Auto 标记）
 * 之后全量闸门仍然 exit 0。
 *
 * 修法不是要求每个作者都记得改用 `bun:test`（那是一条没人会记住的纪律，
 * 而且忘了之后的表现是闸门变绿），是：**解析 errors，一旦某个目录报了
 * errors 就把这个目录逐文件重跑**。逐文件时一个进程只有一个文件，垫片那条
 * 分支不可能触发。常态下（0 errors）一次都不多跑，代价为零。
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
  // 上游 api-key-group-cell.test.tsx 里的 3 条 Auto 动效测试，与本仓改动无关。
  // 曾经登记成 8：另外 5 条其实是被垫片吞掉的**整份文件**，逐文件重跑之后
  // 它们全部变绿（26 条此前从未执行过的断言），真实的上游失败只有 3 条。
  'src/features/keys': 3,
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

/** 收集一个跑测单元下的全部测试文件（逐文件重跑时用）。 */
function collectFiles(suite) {
  const files = []
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      const full = path.join(dir, name)
      if (statSync(full).isDirectory()) {
        if (name === 'node_modules') continue
        walk(full)
        continue
      }
      if (!/\.(test|spec)\.[cm]?[jt]sx?$/.test(name)) continue
      files.push(full.split(path.sep).join('/'))
    }
  }
  walk(path.join(import.meta.dirname, '..', suite))
  return files.sort()
}

/**
 * 跑一次 `bun test <target>` 并把三个计数原样取出来。
 *
 * `errors` 必须解析：bun 把它折进 `N fail` 的同时**另起一行**报
 * `N errors`，而一条 error 意味着整份文件没跑，与"某条断言不成立"完全
 * 不是一回事。`status` 同样要留着：pass/fail 都是 0 而进程非零退出，
 * 说明运行器自己崩了，那时把它当成"这个目录没有失败"是最坏的读法。
 */
function runBun(target) {
  const run = spawnSync('bun', ['test', target], {
    encoding: 'utf8',
    shell: process.platform === 'win32',
  })
  const out = `${run.stdout ?? ''}${run.stderr ?? ''}`
  return {
    out,
    pass: Number(out.match(/(\d+) pass/)?.[1] ?? 0),
    fail: Number(out.match(/(\d+) fail/)?.[1] ?? 0),
    errors: Number(out.match(/(\d+) errors?/)?.[1] ?? 0),
    status: run.status,
  }
}

let totalPass = 0
let totalFail = 0
let unexpected = 0

for (const suite of suites) {
  let result = runBun(suite)
  let note = ''

  if (result.errors > 0) {
    // 目录整批跑撞上了 bun 的 node:test 垫片（describe() inside another
    // test()，oven-sh/bun#5090）：第一个文件之后的每一份都被静默吞掉。
    // 逐文件重跑，一个进程一个文件，那条分支就不可能触发。
    const files = collectFiles(suite)
    console.log(
      `  ..   ${suite}  批量跑报了 ${result.errors} 个 error（整份文件被吞），改为逐文件重跑 ${files.length} 个文件`
    )
    const merged = { out: '', pass: 0, fail: 0, errors: 0, status: 0 }
    for (const file of files) {
      const one = runBun(file)
      merged.out += `
----- ${file} -----
${one.out}`
      merged.pass += one.pass
      merged.fail += one.fail
      merged.errors += one.errors
      if (one.status !== 0 && one.pass === 0 && one.fail === 0)
        merged.status = 1
    }
    result = merged
    note = ' (逐文件重跑)'
  }

  const allowed = KNOWN_FAILURES[suite] ?? 0
  totalPass += result.pass
  totalFail += result.fail

  const crashed = result.pass === 0 && result.fail === 0 && result.status !== 0
  if (result.fail === allowed && result.errors === 0 && !crashed) {
    const known = allowed > 0 ? ` (${allowed} known upstream)` : ''
    console.log(`  ok   ${suite}  ${result.pass} pass${known}${note}`)
    continue
  }
  unexpected++
  console.log(
    `  FAIL ${suite}  ${result.pass} pass, ${result.fail} fail (expected ${allowed})` +
      `${result.errors > 0 ? `, ${result.errors} errors` : ''}` +
      `${crashed ? `, runner exited ${result.status} without running anything` : ''}${note}`
  )
  // 只在对不上的时候打全文：全绿时刷屏会让真正的失败被卷走。
  console.log(result.out.replace(/^/gm, '       '))
}

console.log(
  `\n${totalPass} pass, ${totalFail} fail across ${suites.length} suites`
)
if (unexpected > 0) {
  console.log(`${unexpected} suite(s) did not match the recorded baseline`)
  process.exit(1)
}
