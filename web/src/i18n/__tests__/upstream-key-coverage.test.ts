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
/*
 * 上游风格的 `t('English source string')` 必须在 locales 里有登记。
 *
 * # 为什么需要这一条
 *
 * 本仓已经有 `features/qy/lib/__tests__/i18n-key-coverage.test.ts`，但它的扫描
 * 正则是 `t\(\s*'(qy_[a-z0-9_]+)'`，**只认 qy_ 前缀**。而扩展这一侧在上游的
 * 页面里（兑换码、渠道、系统设置……）加文案时用的是上游风格：键就是英文原文。
 * 那一档从来没有任何守卫：
 *
 *   · 键在 `en.json` 里也没有 ⇒ i18next 回落成**键名本身**，于是中文界面上
 *     出现一整句英文，而英文界面看起来完全正常 —— 评审时最容易放过的形状；
 *   · 带 `{{count}}` / `{{name}}` 的键更糟：拿不到键就不做插值，用户看到的是
 *     字面的 `{{count}} FAQs deleted. Click "Save Settings" to apply.`。
 *
 * 实测查出 10 处这样的键（其中「只有超级管理员可以创建兑换码」那一句是本轮
 * 新加的，其余 9 处更早），它们让 typecheck、lint、既有 i18n 守卫全部保持绿色。
 *
 * # 判据
 *
 * 只认**字面量键**：`t('…')` / `t("…")`。动态键（`t(someVar)`、模板串、
 * `t(qyPayeeChannelKey(x), fallback)`）本来就无法静态对账，交给运行时回落。
 * 带第二个字符串参数的 `t('key', 'default')` 是 i18next 的 defaultValue 形式，
 * 有兜底文案，不入判据。
 *
 * 只查 `en.json`：其余 6 个语种与它的键集合一致由
 * `locale-namespace-shape.test.ts` 与同目录的键集合断言负责，这里不重复。
 */
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const SRC = join(here, '..', '..')

/** 参与扫描的源文件：测试与 __tests__ 目录除外（那里的键是夹具，不上界面）。 */
function listSourceFiles(root: string): string[] {
  const files: string[] = []
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir)) {
      if (entry === 'node_modules' || entry === '__tests__') continue
      const p = join(dir, entry)
      if (statSync(p).isDirectory()) {
        walk(p)
        continue
      }
      if (/\.tsx?$/.test(entry) && !/\.(test|spec)\.tsx?$/.test(entry)) {
        files.push(p)
      }
    }
  }
  walk(root)
  return files.sort()
}

/**
 * 去掉注释再扫。
 *
 * 注释里写 `t('…')` 举例是本仓的常见写法（实测两处），不去掉的话守卫会因为
 * 一句说明而变红，而人最后总是选择把守卫改松。
 */
function stripComments(src: string): string {
  return src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/^[ \t]*\/\/.*$/gm, '')
}

type Site = { file: string; key: string }

function collectLiteralKeys(root: string): Site[] {
  const out: Site[] = []
  // t('key' | "key" 之后：`)` 收尾、`,` 后跟对象/变量都算判据内；
  // `,` 后紧跟引号是 defaultValue 形式，跳过。
  const re = /\bt\(\s*(['"])((?:[^'"\\]|\\.)*)\1\s*(,\s*['"])?/g
  for (const file of listSourceFiles(root)) {
    const src = stripComments(readFileSync(file, 'utf8'))
    let m: RegExpExecArray | null
    while ((m = re.exec(src)) != null) {
      if (m[3] != null) continue
      const key = m[2].replace(/\\(['"\\])/g, '$1')
      if (key.startsWith('qy_')) continue
      out.push({ file: relative(root, file), key })
    }
  }
  return out
}

const en = JSON.parse(
  readFileSync(join(here, '..', 'locales', 'en.json'), 'utf8')
) as { translation: Record<string, string> }

describe('上游风格文案键的覆盖', () => {
  const sites = collectLiteralKeys(SRC)

  test('扫描器自己没坏', () => {
    // 与 route-contract 同一条自检：一个扫不到任何东西的守卫会永远全绿。
    // 实测 5700 余处，留足余量但远高于"提取器失灵"那一档。
    assert.ok(
      sites.length > 3000,
      `只扫到 ${sites.length} 处 t('字面量')，扫描器多半被改坏了`
    )
  })

  test("每一个 t('英文原文') 都在 en.json 里登记过", () => {
    const missing = sites
      .filter((s) => !(s.key in en.translation))
      .map((s) => `${s.file}  ${s.key}`)
    assert.deepEqual(
      [...new Set(missing)],
      [],
      '这些键在 locales/en.json 里不存在：i18next 会把键名原样渲染出来，' +
        '中文界面上就是一整句英文，带 {{}} 的还会把占位符字面打出来。' +
        '补进 7 份 locales（英文那份 value 就等于 key），不要只补 en。'
    )
  })
})
