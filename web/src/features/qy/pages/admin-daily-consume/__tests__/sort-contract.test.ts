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
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * sort-contract.test.ts —— 排序下拉里的每一个值，后端必须真的认识。
 *
 * ## 为什么这条测试非有不可
 *
 * 排序键与路径一样，是前后端之间一个**纯字符串**的契约:它没有类型、没有
 * 编译期检查。而它的失败方式比路径更隐蔽 —— 后端
 * `sortDailyConsume` 对认不出来的键**回落到默认排序**，不报错、不 400。
 * 于是前端把 `consume_qutoa` 拼错一个字母之后:接口 200、表格照常渲染、
 * 只是那个下拉选项从此没有任何效果。没有任何一层会红。
 *
 * 回落本身是对的（拿一个手改坏的 URL 参数把整页打成错误边界更糟），
 * 正因为它是对的，才必须在这里补一条守卫。
 *
 * ## 两侧的数据从哪来
 *
 *   后端：直接读 `qianye/modules/commission/api_daily_consume.go` 里
 *         `dailyConsumeSorts` 那张 map 的键。读源码而不是维护第二份清单 ——
 *         第二份清单会以与被测代码完全相同的方式漂移。
 *   前端：`index.tsx` 里的 `SORT_OPTIONS`。
 */

// __tests__ → admin-daily-consume → pages → qy → features → src → web → repo
const repoRoot = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..',
  '..',
  '..'
)

/** 从 Go 源码里抠出 `dailyConsumeSorts` 那张 map 的键。 */
function backendSortKeys(): string[] {
  const source = readFileSync(
    join(repoRoot, 'qianye', 'modules', 'commission', 'api_daily_consume.go'),
    'utf8'
  )
  const start = source.indexOf('var dailyConsumeSorts')
  assert.ok(start >= 0, '后端的 dailyConsumeSorts 改名了，本测试要跟着改')
  const end = source.indexOf('\n}', start)
  assert.ok(end > start, 'dailyConsumeSorts 的 map 字面量没有正常闭合')
  const body = source.slice(start, end)
  return [...body.matchAll(/^\t"([a-z_]+)":/gm)].map((m) => m[1]).sort()
}

/** 从 `index.tsx` 里抠出 `SORT_OPTIONS`。 */
function frontendSortKeys(): string[] {
  const source = readFileSync(
    join(dirname(fileURLToPath(import.meta.url)), '..', 'index.tsx'),
    'utf8'
  )
  const decl = source.indexOf('const SORT_OPTIONS')
  assert.ok(decl >= 0, 'SORT_OPTIONS 改名了，本测试要跟着改')
  // 从 `= [` 开始，不是从声明开始:类型标注里那对 `QyDailyConsumeSort[]`
  // 的方括号会先撞上，切出来的是一个空片段 —— 而空片段与"两侧都没有排序键"
  // 无法区分，这条守卫会变成永真。
  const start = source.indexOf('= [', decl)
  assert.ok(start > decl, 'SORT_OPTIONS 不再是一个数组字面量了')
  const end = source.indexOf(']', start)
  assert.ok(end > start, 'SORT_OPTIONS 的数组字面量没有正常闭合')
  return [...source.slice(start, end).matchAll(/'([a-z_]+)'/g)]
    .map((m) => m[1])
    .sort()
}

describe('qy 日消费明细排序契约', () => {
  test('下拉里的每一个排序键后端都认识', () => {
    const backend = backendSortKeys()
    assert.ok(backend.length > 0, '没从 Go 源码里读出任何排序键，抠法失效了')
    assert.deepEqual(
      frontendSortKeys(),
      backend,
      '排序键两侧对不上：后端认不出来的键会**静默回落到默认排序**，' +
        '接口照常 200、表格照常渲染，只是那个选项从此没有任何效果'
    )
  })

  test('每个排序键都有对应的中英文案', () => {
    const enKeys = en as Record<string, string>
    const zhKeys = zh as Record<string, string>
    for (const key of frontendSortKeys()) {
      // 缺键时 i18next 会把**键名本身**渲染出来，也就是下拉里出现一行
      // `qy_dc_sort_consume_quota`，而 typecheck 与其它测试全绿。
      assert.ok(
        enKeys[`qy_dc_sort_${key}`] != null,
        `en.json 缺少 qy_dc_sort_${key}`
      )
      assert.ok(
        zhKeys[`qy_dc_sort_${key}`] != null,
        `zh.json 缺少 qy_dc_sort_${key}`
      )
    }
  })
})
