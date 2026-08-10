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
 * 模型广场的「未登录预览」。
 *
 * # 守什么
 *
 * 后端在未登录时按「新用户注册后落进的默认用户分组」渲染模型与倍率
 * （controller/qy_plaza_viewer.go），并且在那一档确实没有任何可用模型分组时
 * **刻意返回空列表而不是回落全量**。这条选择只有配上一句解释才不误导：
 *
 *   1. 有内容时要说明"这一页算的是谁的价"，否则一个属于别的分组的用户退出登录
 *      之后会看到另一套价格，而页面上没有任何东西解释为什么；
 *   2. 空的时候**不能**显示"换个筛选条件试试" —— 清空筛选不会有任何变化，
 *      那是把人往死路上引。
 *
 * 两件事各自的判据都在 `plazaEmptyReason` 与页面对 `anonymousPreview` 的消费上，
 * 因此这里两边一起断言：纯函数的三个分支 + 页面真的接上了它们。
 * 只测纯函数是本仓反复出现的"变量算对了但没人用"。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { plazaEmptyReason } from '@/features/pricing/lib/plaza-scope'

import qyEn from '../../../i18n/qy/en.json'
import qyZh from '../../../i18n/qy/zh.json'

const here = dirname(fileURLToPath(import.meta.url))
const webSrc = join(here, '..', '..', '..')

const readSource = (relative: string): string =>
  readFileSync(join(webSrc, relative), 'utf8')

describe('plazaEmptyReason', () => {
  const cases: Array<{
    name: string
    input: Parameters<typeof plazaEmptyReason>[0]
    want: ReturnType<typeof plazaEmptyReason>
  }> = [
    {
      name: '有筛选结果时没有空态',
      input: { anonymousPreview: true, totalModels: 12, filteredModels: 3 },
      want: 'none',
    },
    {
      name: '未登录 + 后端一个模型都没给 => 默认分组无可用模型',
      input: { anonymousPreview: true, totalModels: 0, filteredModels: 0 },
      want: 'anonymous-scope',
    },
    {
      name: '未登录但后端给了模型、是用户自己筛没的 => 筛选空态',
      input: { anonymousPreview: true, totalModels: 12, filteredModels: 0 },
      want: 'filters',
    },
    {
      name: '已登录且一个模型都没有 => 仍走筛选空态（那是他自己那一档的事实，与预览无关）',
      input: { anonymousPreview: false, totalModels: 0, filteredModels: 0 },
      want: 'filters',
    },
  ]

  for (const item of cases) {
    test(item.name, () => {
      assert.equal(plazaEmptyReason(item.input), item.want)
    })
  }
})

describe('模型广场页面真的消费了这两样', () => {
  const page = readSource('features/pricing/index.tsx')

  test('未登录预览提示挂在页面上', () => {
    assert.match(page, /anonymousPreview\s*&&/)
    assert.match(page, /qy_plaza_anon_preview_notice/)
  })

  test('空态按成因分叉，anonymous-scope 不复用筛选文案', () => {
    assert.match(page, /plazaEmptyReason\(/)
    assert.match(page, /emptyReason === 'anonymous-scope'/)
    assert.match(page, /qy_plaza_anon_empty_title/)
    assert.match(page, /qy_plaza_anon_empty_desc/)
  })

  test('anonymousPreview 来自后端字段而不是前端自己猜登录态', () => {
    const hook = readSource('features/pricing/hooks/use-pricing-data.ts')
    assert.match(hook, /anonymousPreview:\s*data\?\.anonymous_preview/)
  })
})

describe('三个 qy 键 zh/en 都登记了', () => {
  const keys = [
    'qy_plaza_anon_preview_notice',
    'qy_plaza_anon_empty_title',
    'qy_plaza_anon_empty_desc',
  ]

  for (const key of keys) {
    test(key, () => {
      assert.equal(typeof (qyZh as Record<string, string>)[key], 'string')
      assert.equal(typeof (qyEn as Record<string, string>)[key], 'string')
      assert.notEqual((qyZh as Record<string, string>)[key], '')
      assert.notEqual((qyEn as Record<string, string>)[key], '')
    })
  }
})
