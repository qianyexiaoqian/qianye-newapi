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
// scripts/format-with-protected-headers.mjs 的回归。
//
// 这个脚本必须先摘掉版权头才能跑 oxfmt(`sortImports` 会把版权头当成第一条
// import 的前导注释、跟着它一起挪走)。旧实现是**在工作树里原地摘**,于是
// 存在一段"树上没有版权头"的窗口 —— 两个进程交错时,后一个进程会把无头版
// 当成基线快照下来再写回去,实测一次抹掉 446 个文件的版权头,
// 直接违反 AGENTS.md 的受保护标识条款。
//
// 三条判据,合起来盯住的是同一件事「工作树在任何时刻都带着版权头」:
//   1. `--check` 一个字节都不许写工作树;
//   2. `--write` 只写内容真的变了的文件;
//   3. 两个并发实例跑完,每个文件的版权头都还在。
//
// 1 和 2 用 mtime 判 —— 内容相等骗得过去(旧实现摘完再贴回来,内容是相等的),
// mtime 骗不过去。这两条是**确定性**的:对着旧实现跑 5 次,5 次全红。
// 第 3 条是端到端的那一条,但它红不红取决于两个进程的交错,实测对着旧实现
// 5 次里红 3 次 —— 所以它是补充,不是主判据;真正把缺陷钉死的是 1 和 2。
import assert from 'node:assert/strict'
import { spawn, spawnSync } from 'node:child_process'
import {
  copyFileSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { delimiter, join } from 'node:path'
import { after, describe, test } from 'node:test'

const webRoot = join(import.meta.dirname, '..', '..')
const script = join(webRoot, 'scripts', 'format-with-protected-headers.mjs')
const binDir = join(webRoot, 'node_modules', '.bin')

const PROTECTED_HEADER = `/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

For commercial licensing, please contact support@quantumnous.com
*/
`

// 相对 import 在前、外部包在后:oxfmt 的 sortImports 必须把两条 import 换位,
// 而版权头正是挂在第一条 import 上的前导注释 —— 这就是必须先摘头的原因。
// 正文同时留着 oxfmt 会改的格式问题(双引号、多余空格),确保 oxfmt 真的重写文件。
function fixtureSource(index: number): string {
  return `${PROTECTED_HEADER}import { helper } from "./helper-${index}"
import { z } from "zod"
export const value${index}  =  z.string( ).parse( helper )
`
}

const fixtures: string[] = []

after(() => {
  for (const dir of fixtures) {
    rmSync(dir, { force: true, recursive: true })
  }
})

function makeFixture(fileCount: number): string {
  const dir = mkdtempSync(join(tmpdir(), 'qy-format-lock-'))
  fixtures.push(dir)
  mkdirSync(join(dir, 'src'), { recursive: true })
  copyFileSync(join(webRoot, '.oxfmtrc.json'), join(dir, '.oxfmtrc.json'))
  copyFileSync(join(webRoot, '.gitignore'), join(dir, '.gitignore'))
  for (let i = 0; i < fileCount; i++) {
    writeFileSync(join(dir, 'src', `file-${i}.ts`), fixtureSource(i))
  }
  return dir
}

function fixtureFiles(dir: string, fileCount: number): string[] {
  return Array.from({ length: fileCount }, (_, i) =>
    join(dir, 'src', `file-${i}.ts`)
  )
}

function formatEnv() {
  return {
    ...process.env,
    PATH: `${binDir}${delimiter}${process.env.PATH ?? ''}`,
  }
}

function runFormat(dir: string, mode: '--check' | '--write') {
  return spawnSync(process.execPath, [script, mode], {
    cwd: dir,
    encoding: 'utf8',
    env: formatEnv(),
  })
}

function runFormatAsync(
  dir: string,
  mode: '--check' | '--write'
): Promise<number | null> {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [script, mode], {
      cwd: dir,
      env: formatEnv(),
      stdio: 'ignore',
    })
    child.on('error', reject)
    child.on('close', (code) => resolve(code))
  })
}

describe('format-with-protected-headers', () => {
  test('--check never writes to the working tree', () => {
    const fileCount = 8
    const dir = makeFixture(fileCount)
    const files = fixtureFiles(dir, fileCount)
    const before = files.map((file) => {
      const stat = statSync(file)
      return { mtimeMs: stat.mtimeMs, size: stat.size }
    })

    const result = runFormat(dir, '--check')
    // 夹具是故意没格式化好的,--check 必须报出问题(退出码 1),
    // 否则下面的"没写过"就可能只是因为 oxfmt 压根没跑起来。
    assert.equal(result.status, 1, result.stderr)
    assert.match(result.stderr, /Format issues found/)

    files.forEach((file, index) => {
      const stat = statSync(file)
      // 内容相等骗得过去(旧实现摘完再贴回来),mtime 骗不过去。
      assert.equal(
        stat.mtimeMs,
        before[index]?.mtimeMs,
        `${file} was written by a read-only --check`
      )
      assert.equal(stat.size, before[index]?.size)
      assert.ok(readFileSync(file, 'utf8').startsWith(PROTECTED_HEADER))
    })
  })

  test('--write only touches the files it actually changes', () => {
    const fileCount = 8
    const dir = makeFixture(fileCount)
    const files = fixtureFiles(dir, fileCount)

    // 第一遍把夹具格式化到不动点。
    assert.equal(runFormat(dir, '--write').status, 0)
    const settled = files.map((file) => ({
      content: readFileSync(file, 'utf8'),
      mtimeMs: statSync(file).mtimeMs,
    }))
    for (const entry of settled) {
      assert.ok(entry.content.startsWith(PROTECTED_HEADER))
    }

    // 第二遍无事可做,于是一个文件都不该被写。
    //
    // 这条不是洁癖:旧实现无论有没有改动都会把每个带版权头的文件写两次
    // (摘头一次、贴回一次),那两次写之间工作树是**没有版权头**的。
    // 「只写真的变了的文件」等价于「不存在无头窗口」—— 并发安全就是从这里来的,
    // 而不是靠一把锁把别人挡在外面(锁挡不住同时在读的编辑器、tsgo、测试进程,
    // 也挡不住进程被 kill 在窗口正中间)。
    assert.equal(runFormat(dir, '--write').status, 0)
    files.forEach((file, index) => {
      assert.equal(readFileSync(file, 'utf8'), settled[index]?.content)
      assert.equal(
        statSync(file).mtimeMs,
        settled[index]?.mtimeMs,
        `${file} was rewritten by a no-op --write`
      )
    })
  })

  test(
    'two concurrent runs both keep every protected header',
    { timeout: 120_000 },
    async () => {
      // 文件数要够多,两个进程的"摘头 → oxfmt → 贴回"窗口才会真的重叠;
      // 同时必须用异步 spawn —— spawnSync 会把事件循环整个堵住,
      // 两个 Promise 实际是先后跑的,那样测的根本不是并发。
      const fileCount = 400
      const dir = makeFixture(fileCount)
      const files = fixtureFiles(dir, fileCount)

      const exitCodes = await Promise.all([
        runFormatAsync(dir, '--write'),
        runFormatAsync(dir, '--check'),
      ])
      for (const code of exitCodes) {
        assert.ok(code !== null, 'both runs must exit')
      }

      const missing = files.filter(
        (file) => !readFileSync(file, 'utf8').startsWith(PROTECTED_HEADER)
      )
      assert.deepEqual(
        missing.map((file) => file.slice(dir.length + 1)),
        [],
        'concurrent runs must not drop any protected copyright header'
      )
    }
  )

  test('oxfmt 起不来时必须说话，而且退出码要与「有文件没格式化」分开', () => {
    /*
      spawnSync 在 ENOENT 时 status 是 null、error 才带着原因，而
      stdio:'inherit' 意味着 Node 一个字节都没写。此前这里是
      `result.status ?? 1`：null 被折叠成 1，而 result.error 从头到尾没人读，
      于是脚本能在 stdout 与 stderr **都是 0 字节**的情况下退出 1 —— 与
      「发现格式问题」在退出码上不可区分。--write 模式更糟：它同样 exit 1
      且一个文件都没格式化，调用方却以为自己刚跑过一次格式化。

      而这条路正是脚本自己的 usage 串教出来的：
      `node scripts/format-with-protected-headers.mjs --check|--write`
      —— 不经 bun/npm 直接跑，node_modules/.bin 就不在 PATH 里。

      两条判据缺一不可：退出码要能区分（否则调用方分不出这两件事），
      stderr 要有话（否则人查不出原因）。
    */
    const dir = makeFixture(2)
    for (const mode of ['--check', '--write'] as const) {
      const run = spawnSync(process.execPath, [script, mode], {
        cwd: dir,
        encoding: 'utf8',
        // 刻意**不**注入 node_modules/.bin —— 这就是被复现的那条路。
        env: { ...process.env, PATH: '' },
      })
      assert.equal(
        run.status,
        2,
        `${mode}: 起不来必须用一个与「有文件没格式化」(1) 不同的退出码`
      )
      assert.notEqual(
        (run.stderr ?? '').trim(),
        '',
        `${mode}: 一个字节都不说的失败查不出原因`
      )
      assert.match(run.stderr, /oxfmt/, `${mode}: 失败原因里要点名是谁没跑起来`)
    }
  })
})
