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
import assert from 'node:assert/strict'
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

const localesDir = join(dirname(fileURLToPath(import.meta.url)), '..', 'locales')

/**
 * 语言包的顶层只能有 `translation` 这一个键。
 *
 * i18next 把资源的顶层键当**命名空间**，而 config.ts 里默认命名空间是
 * `translation`。写在 `translation` 之外的键因此在任何语言下都解析不到，
 * 只会回落成键名本身：en/zh 末尾曾各有三行 `Order` / `Move up` / `Move down`
 * 与 `translation` 平级，zh 里明明写着「排序/上移/下移」，用户在 7 个语种下
 * 看到的全是英文原文（英文用户看不出异常，因为键恰好等于译文），而
 * `bun run typecheck` 与全量测试都是绿的。
 *
 * 这条断言比"某几个键存在"更值得写：它挡的是**形状**，任何人日后再往文件
 * 尾巴上追加一个键都会立刻红。
 */
describe('语言包命名空间形状', () => {
  const files = readdirSync(localesDir).filter((f) => f.endsWith('.json'))

  test('至少扫到 7 个语种，否则这份守卫已经脱靶', () => {
    assert.ok(files.length >= 7, `只扫到 ${files.length} 份语言包`)
  })

  for (const file of files) {
    test(`${file} 顶层只有 translation`, () => {
      const raw = JSON.parse(readFileSync(join(localesDir, file), 'utf8')) as Record<string, unknown>
      const extras = Object.keys(raw).filter((k) => k !== 'translation')
      assert.deepEqual(
        extras,
        [],
        `${file} 的顶层出现了 ${extras.join(', ')}：` +
          'i18next 会把它们当成命名空间，于是这些键在任何语言下都解析不到，' +
          '页面上显示的是键名本身',
      )
      assert.equal(typeof raw.translation, 'object')
    })
  }

  test('排序按钮的三个键在 7 个语种里都在 translation 内', () => {
    for (const file of files) {
      const raw = JSON.parse(readFileSync(join(localesDir, file), 'utf8')) as {
        translation: Record<string, string>
      }
      for (const key of ['Order', 'Move up', 'Move down']) {
        assert.ok(
          typeof raw.translation[key] === 'string' && raw.translation[key].length > 0,
          `${file} 缺少 ${key}`,
        )
      }
    }
  })
})
