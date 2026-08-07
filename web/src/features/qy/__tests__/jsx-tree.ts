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
 * JSX 挂载点断言的公共零件（**测试专用**，不是产品代码）。
 *
 * 本仓反复出现的缺陷形状之一是"组件写好了却挂在插槽外/根本没被挂上"，
 * 而 `source.includes('Foo')` 挡不住它 —— 注释、import、条件渲染里都会出现
 * 这个名字。所以挂载点断言一律解析 AST，按 JSX 祖先链判定。
 *
 * 这份走查逻辑现在有三个消费方（钱包页的划转格、推荐计划的搬家、划转流水的
 * 余额快照），各抄一份就是本仓的另一种缺陷形状"同一概念的第 N 份拷贝"，
 * 所以收在这里。文件名不带 `.test.`，`bun test` 不会把它当用例文件收集。
 *
 * 用 `oxc-parser` 而不是正则数尖括号；它不可用时 import 会直接抛错，
 * 不会静默跳过。
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'

import { parseSync } from 'oxc-parser'

type JsxNode = Record<string, unknown>

function jsxName(node: JsxNode): string | null {
  const opening = node.openingElement as JsxNode | undefined
  const name = opening?.name as JsxNode | undefined
  if (name == null) return null
  if (name.type === 'JSXIdentifier') return name.name as string
  // <Foo.Bar /> —— 本走查只关心裸标识符，成员表达式一律记成完整写法。
  if (name.type === 'JSXMemberExpression') {
    const object = name.object as JsxNode
    const property = name.property as JsxNode
    return `${object.name as string}.${property.name as string}`
  }
  return null
}

export type JsxTree = {
  /** 源码原文，供"这里不该再出现某段字面量"这类断言使用。 */
  source: string
  /** 组件名 → 它每一次出现时的祖先 JSX 标签名列表（由外到内）。 */
  occurrences: (name: string) => string[][]
}

/** 解析一个 .tsx 文件，返回可按 JSX 祖先链查询的走查结果。 */
export function readJsxTree(path: string): JsxTree {
  const source = readFileSync(path, 'utf8')
  const parsed = parseSync(path, source)
  assert.deepEqual(parsed.errors, [], `解析失败：${path}`)

  const found = new Map<string, string[][]>()
  const stack: string[] = []

  const visit = (node: unknown) => {
    if (node == null || typeof node !== 'object') return
    if (Array.isArray(node)) {
      for (const child of node) visit(child)
      return
    }
    const current = node as JsxNode
    const isElement = current.type === 'JSXElement'
    let name: string | null = null
    if (isElement) {
      name = jsxName(current)
      if (name != null) {
        const list = found.get(name) ?? []
        list.push([...stack])
        found.set(name, list)
        stack.push(name)
      }
    }
    for (const [key, value] of Object.entries(current)) {
      if (key === 'type' || key === 'start' || key === 'end') continue
      visit(value)
    }
    if (isElement && name != null) stack.pop()
  }

  visit(parsed.program)

  return { source, occurrences: (name) => found.get(name) ?? [] }
}
