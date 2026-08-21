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
 * 「API 地址 + 复制 / 带 V1 复制」这条控件必须挂在密钥页上。
 *
 * # 为什么单独守一条
 *
 * `copy-bar.test.tsx` 把控件本身从头点到尾，全绿；但它证明不了**有人把它
 * 摆出来了**。控件写好却没挂上去，正是本仓复发过五次以上的那个形状：
 * typecheck 绿、单测绿、界面上什么都没有。删掉 `<QyApiAddressCopyBar />`
 * 这一行不会让任何别的测试变红。
 *
 * 挂在密钥页而不是别处：用户来这一页就是为了拿"密钥 + 地址"这一对。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { parseSync } from 'oxc-parser'

// __tests__ → api-address-picker → pages → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..'
)
const keysPagePath = join(srcDir, 'features/keys/index.tsx')

describe('API 地址复制控件挂在密钥页上', () => {
  test('密钥页渲染了 QyApiAddressCopyBar', () => {
    const source = readFileSync(keysPagePath, 'utf8')
    const parsed = parseSync(keysPagePath, source)
    assert.deepEqual(parsed.errors, [], `解析失败：${keysPagePath}`)

    const rendered: string[] = []
    const walk = (node: unknown) => {
      if (node == null || typeof node !== 'object') return
      if (Array.isArray(node)) {
        for (const child of node) walk(child)
        return
      }
      const current = node as Record<string, unknown>
      if (current.type === 'JSXOpeningElement') {
        const name = (current.name as { name?: string } | undefined)?.name
        if (typeof name === 'string') rendered.push(name)
      }
      for (const value of Object.values(current)) walk(value)
    }
    walk(parsed.program)

    assert.ok(
      rendered.includes('QyApiAddressCopyBar'),
      '密钥页上没有 API 地址复制控件 —— 控件写好了却没有任何入口能看到它'
    )
    assert.ok(
      rendered.includes('ApiKeysTable'),
      '密钥表不见了 —— 这条断言只是确认上面那条查的确实是这一页'
    )
  })
})
