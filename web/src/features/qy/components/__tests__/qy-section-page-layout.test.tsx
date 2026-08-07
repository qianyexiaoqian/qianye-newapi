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
 * qy-section-page-layout —— 插槽外的 children 必须仍然被渲染。
 *
 * # 这条测试守的是什么
 *
 * 上游 `SectionPageLayout` 用 `Children.forEach` + `child.type ===` 认插槽，
 * **认不出的 children 连引用都不保留**（section-page-layout.tsx 的四个
 * if/else if 之外没有 else，非插槽节点直接被丢掉）。
 *
 * 而 qy 的页面普遍这么写：
 *
 *     <QySectionPageLayout.Content>…</QySectionPageLayout.Content>
 *     <QyGroupRuleFormSheet … />      ← 插槽外
 *     <QyConfirmDialog … />
 *
 * 于是这些页面上所有的确认框、编辑抽屉、详情弹窗**全是死的**：点按钮确实改了
 * state，但组件压根不在 DOM 里，什么都不会发生。项目方报的「划转分组点新建规则
 * 没反应」就是它的一个表现，实测受影响 14 个页面。
 *
 * 修在唯一的公共外壳上而不是逐页把弹窗塞进 <Content>：后者是 14 个文件的改动，
 * 而且下一个新页面照着现有页面抄还会再犯 —— 那正是本项目累计出现十几次的
 * 「写了但没接上」形状。
 *
 * # 关于"只渲染一次"那一条 —— 它现在是一张保险，不是当下的防线
 *
 * 我原本以为它能挡住「把 children 原样再渲染一遍」这种错误修法。实测**挡不住**：
 * 四个插槽标记组件在上游都是 `return null` 的纯标记（section-page-layout.tsx:32-50），
 * 所以直接渲染 `{props.children}` 与只渲染收集出来的 `{rest}` 行为完全等价，
 * 变异之后测试照样全绿。
 *
 * 保留它的理由变成了：一旦上游让插槽组件自己渲染内容（把 `return null` 改成
 * `return props.children`），「原样再渲染一遍」就会立刻变成重复渲染，那时这条会响。
 * 如实写在这里，免得下一个人以为它正在守着什么。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window()
const domGlobals = [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'CustomEvent',
  'MutationObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
] as const

for (const key of domGlobals) {
  Object.defineProperty(globalThis, key, {
    configurable: true,
    value: domWindow[key],
  })
}

const { act, Children, isValidElement } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const { SectionPageLayout } =
  await import('../../../../components/layout/components/section-page-layout')

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const roots: Array<{
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
}> = []

async function mount(node: React.ReactNode) {
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(node)
  })
  roots.push({ container, root })
  return container
}

after(async () => {
  for (const { container, root } of roots) {
    await act(async () => root.unmount())
    container.remove()
  }
})

// 用最小可用的替身而不是真的 QySectionPageLayout：后者依赖 TanStack Router 的
// useLocation 与主题 hook，在这个测试环境里挂不起来。这里验证的是**收集非插槽
// children 并与布局并列渲染**这一条机制本身，它与主题/路由无关。
// QySectionPageLayout 的实现必须与这里保持同一形状 —— 见下面第三条断言。
function Shell(props: { children?: React.ReactNode }) {
  const slots = new Set<unknown>([
    SectionPageLayout.Title,
    SectionPageLayout.Actions,
    SectionPageLayout.Content,
    SectionPageLayout.Breadcrumb,
  ])
  const rest: React.ReactNode[] = []
  Children.forEach(props.children, (n: React.ReactNode) => {
    if (!isValidElement(n)) return
    if (slots.has(n.type)) return
    rest.push(n)
  })
  return (
    <>
      <SectionPageLayout>{props.children}</SectionPageLayout>
      {rest}
    </>
  )
}

describe('插槽外的 children', () => {
  test('上游 SectionPageLayout 确实会把它们丢掉（本文件存在的理由）', async () => {
    const c = await mount(
      <SectionPageLayout>
        <SectionPageLayout.Content>正文</SectionPageLayout.Content>
        <div data-testid='dropped'>弹窗</div>
      </SectionPageLayout>
    )
    // 哪天上游改成保留非插槽 children，这一条会变红 —— 那时 QySectionPageLayout
    // 里的收集逻辑必须删掉，否则会渲染两遍。
    assert.equal(c.querySelectorAll('[data-testid="dropped"]').length, 0)
  })

  test('外壳收集之后必须出现', async () => {
    const c = await mount(
      <Shell>
        <SectionPageLayout.Content>正文</SectionPageLayout.Content>
        <div data-testid='dialog'>弹窗</div>
      </Shell>
    )
    assert.equal(c.querySelectorAll('[data-testid="dialog"]').length, 1)
  })

  test('插槽内容只出现一次（防"再渲染一遍"式的错误修法）', async () => {
    const c = await mount(
      <Shell>
        <SectionPageLayout.Title>唯一标题</SectionPageLayout.Title>
        <SectionPageLayout.Content>唯一正文</SectionPageLayout.Content>
        <div data-testid='dialog'>弹窗</div>
      </Shell>
    )
    const text = c.textContent ?? ''
    assert.equal(text.split('唯一标题').length - 1, 1)
    assert.equal(text.split('唯一正文').length - 1, 1)
    assert.equal(c.querySelectorAll('[data-testid="dialog"]').length, 1)
  })
})
