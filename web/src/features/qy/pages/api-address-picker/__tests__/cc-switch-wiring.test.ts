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
 * 「CC Switch」这个入口**必须**先经过线路选择。
 *
 * # 为什么要一条源码级的守卫
 *
 * 项目方两轮反馈是同一件事的两半：先是「复制链接信息」要先选线路，随后是
 * 「用户在 API 密钥页面，填入 CC Switch，这个选项也要先弹出选择 API 线路的
 * 窗口选择线路后再弹出 CC Switch 的配置窗口」。第二半之所以要单独提，正是因为
 * 第一半做完之后 CC Switch 那条路仍然直挂在 `setOpen('cc-switch')` 上 ——
 * 两个入口就差这么一行，谁也不会注意到。
 *
 * 绕过它的代价是**静默**的：入口照样能点，配置窗口照样弹，只是里面的接口
 * 地址永远是系统设置里那一个。用户把它导进客户端、跑一段时间之后才发现自己
 * 根本没走上想走的那条线路。typecheck 全绿，交互测试（另一份文件）测的是
 * 组合好之后的行为、测不到"真实的那个入口到底调了谁"。
 *
 * 所以这里对着 AST 钉死三件事：
 *
 *   1. CC Switch 按钮的 onClick 里只有 `pickCcSwitchAddress(...)`，
 *      没有任何 `setOpen(` —— 它没有能力直接掀开配置窗口。
 *   2. 全文件里 `setOpen('cc-switch')` 只出现一次，且落在传给
 *      `useQyApiAddressPicker` 的那个对象字面量里 —— 也就是"选完线路之后"。
 *   3. 选中的地址真的被交出去了：`setCcSwitchAddress` 在同一个回调里被调用，
 *      并且 `ApiKeysDialogs` 把它当作 `apiAddress` 传给了配置窗口。
 *      少了这一步，用户选了线路、配置里却还是站点地址 —— 这正是最难发现的那种错。
 *
 * # 入口从菜单项变成了操作列里的按钮
 *
 * 项目方后来又提：「API密钥那把【一键导入CC Switch】做成一个按钮显示在
 * 操作列。」——原先它是「⋯」下拉里的一项，属于本仓复发过多次的「实现了但
 * 用户点不到」。所以这份守卫顺带钉住**它不能再退回下拉菜单**：一旦有人把
 * 它挪回 `DropdownMenuItem`，这里就红。它真的渲染在操作列里（而不是只在
 * 源码里存在）由 `cc-switch-entry-render.test.tsx` 用一次真实渲染守。
 *
 * 断的是 AST 而不是正则：注释、字符串、格式化换行都不会让它误报或漏报。
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
const rowActionsPath = join(
  srcDir,
  'features/keys/components/data-table-row-actions.tsx'
)
const dialogsPath = join(
  srcDir,
  'features/keys/components/api-keys-dialogs.tsx'
)

type Node = Record<string, unknown>

function walk(root: unknown, visit: (node: Node) => void) {
  const seen = (node: unknown) => {
    if (node == null || typeof node !== 'object') return
    if (Array.isArray(node)) {
      for (const child of node) seen(child)
      return
    }
    const current = node as Node
    if (typeof current.type === 'string') visit(current)
    for (const value of Object.values(current)) seen(value)
  }
  seen(root)
}

function sourceOf(path: string) {
  const source = readFileSync(path, 'utf8')
  const parsed = parseSync(path, source)
  assert.deepEqual(parsed.errors, [], `解析失败：${path}`)
  const slice = (node: unknown) => {
    const span = node as { start: number; end: number }
    return source.slice(span.start, span.end)
  }
  return { source, program: parsed.program, slice }
}

/**
 * 取「CC Switch」那个按钮的 onClick 源码。
 *
 * 认的是 `aria-label={t('CC Switch')}`，而不是变量名或出现顺序：按钮换位置、
 * 回调改名都不该让这条守卫失效，而文案一改就是另一个入口了。图标按钮没有可见
 * 文本，`aria-label` 就是它对用户/读屏软件公开的那个名字。
 */
function ccSwitchButtonOnClick(): string {
  const { program, slice } = sourceOf(rowActionsPath)
  const found: string[] = []
  walk(program, (node) => {
    if (node.type !== 'JSXOpeningElement') return
    const name = (node.name as { name?: string } | undefined)?.name
    if (name !== 'Button') return
    const attrs = (node.attributes ?? []) as Node[]
    const label = attrs.find(
      (a) => (a.name as { name?: string } | undefined)?.name === 'aria-label'
    )
    if (label == null || slice(label.value) !== "{t('CC Switch')}") return
    const onClick = attrs.find(
      (a) => (a.name as { name?: string } | undefined)?.name === 'onClick'
    )
    assert.ok(onClick != null, 'CC Switch 按钮没有 onClick')
    found.push(slice(onClick.value))
  })
  assert.equal(
    found.length,
    1,
    `操作列里应当恰好有一个「CC Switch」按钮，找到 ${found.length} 个`
  )
  return found[0]
}

/** 取传给 `useQyApiAddressPicker(...)` 的那个参数的源码。 */
function apiAddressPickerCallArgument(): string {
  const { program, slice } = sourceOf(rowActionsPath)
  const found: string[] = []
  walk(program, (node) => {
    if (node.type !== 'CallExpression') return
    const callee = node.callee as { name?: string } | undefined
    if (callee?.name !== 'useQyApiAddressPicker') return
    const args = node.arguments as unknown[]
    assert.equal(args.length, 1, 'useQyApiAddressPicker 应当只收一个配置对象')
    found.push(slice(args[0]))
  })
  assert.equal(
    found.length,
    1,
    `密钥行内应当恰好接一次线路选择，找到 ${found.length} 次`
  )
  return found[0]
}

describe('CC Switch 入口必须先经过线路选择', () => {
  test('按钮自己掀不开配置窗口，只能把密钥交给线路选择', () => {
    const onClick = ccSwitchButtonOnClick()
    assert.ok(
      onClick.includes('pickCcSwitchAddress('),
      `「CC Switch」按钮绕过了线路选择，直接就把配置窗口打开了：\n${onClick}`
    )
    assert.ok(
      !onClick.includes('setOpen('),
      `「CC Switch」按钮自己调了 setOpen —— 那就是把选线路这一步跳过去了：\n${onClick}`
    )
  })

  test('入口留在操作列里，不许退回「⋯」下拉', () => {
    const { program, slice } = sourceOf(rowActionsPath)
    const inMenu: string[] = []
    walk(program, (node) => {
      if (node.type !== 'JSXElement') return
      const opening = node.openingElement as Node | undefined
      const name = (opening?.name as { name?: string } | undefined)?.name
      if (name !== 'DropdownMenuItem') return
      if (!slice(node).includes("t('CC Switch')")) return
      inMenu.push(slice(node))
    })
    assert.deepEqual(
      inMenu,
      [],
      '「CC Switch」又被收回下拉菜单里了 —— 项目方要的是操作列上一个直接能点的按钮'
    )
  })

  test('配置窗口只在选完线路之后打开，且选中的地址一起交出去', () => {
    const { source } = sourceOf(rowActionsPath)
    const occurrences = source.split("setOpen('cc-switch')").length - 1
    assert.equal(
      occurrences,
      1,
      `setOpen('cc-switch') 出现了 ${occurrences} 次；多一处就是多一条绕过线路选择的路`
    )

    const picker = apiAddressPickerCallArgument()
    assert.ok(
      picker.includes("setOpen('cc-switch')"),
      `配置窗口不是在选完线路之后打开的：\n${picker}`
    )
    assert.ok(
      picker.includes('setCcSwitchAddress(url)'),
      `选中的那条线路没有被交给配置窗口，配置里的地址会退回站点地址：\n${picker}`
    )
  })

  test('配置窗口拿到的 apiAddress 就是选中的那条线路', () => {
    const { program, slice } = sourceOf(dialogsPath)
    const found: string[] = []
    walk(program, (node) => {
      if (node.type !== 'JSXOpeningElement') return
      const tag = (node.name as { name?: string } | undefined)?.name
      if (tag !== 'CCSwitchDialog') return
      const attrs = (node.attributes ?? []) as Node[]
      const apiAddress = attrs.find(
        (a) => (a.name as { name?: string } | undefined)?.name === 'apiAddress'
      )
      assert.ok(
        apiAddress != null,
        'CCSwitchDialog 没有收到 apiAddress —— 配置里的地址会与用户选的那条无关'
      )
      found.push(slice(apiAddress.value))
    })
    assert.equal(found.length, 1, 'CC Switch 配置窗口应当只挂一处')
    assert.equal(found[0], '{ccSwitchAddress}')
  })
})
