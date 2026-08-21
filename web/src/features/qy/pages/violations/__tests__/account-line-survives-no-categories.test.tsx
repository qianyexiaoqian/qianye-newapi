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
 * 一个类型都没公示时，**账号总量线**不能跟着一起消失。
 *
 * 缺陷形状：`items.length === 0` 的提前 return 把整张卡片收起来，而
 * `account_threshold` / `account_hit_count` / `account_window_hours` 就在同一份
 * 响应里、和 items 一点关系都没有 —— 它们来自 resolveBanPolicy 与全局计数器。
 * 而**未公示的类型照样计数、照样触发处置**（后端 userCategoryLines 刻意不看
 * published）。
 *
 * 于是在「不公示任何类型 + 设了账号总量门槛」这一种合法配置下：三块统计已经
 * 被移除，唯一剩下的预警渠道又被这行 guard 关掉，一个已经 9/10 的用户在这一页
 * 上看不到任何倒计时，然后毫无预警地被封。管理员在管理端取消公示时，也不会有
 * 任何提示说明连账号线一起被关掉了。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import { Window } from 'happy-dom'

import qyZh from '@/i18n/qy/zh.json'

const domWindow = new Window({ width: 1280, height: 900 })
for (const key of [
  'window',
  'document',
  'navigator',
  'localStorage',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'MutationObserver',
  'ResizeObserver',
  'requestAnimationFrame',
  'cancelAnimationFrame',
  'getComputedStyle',
  'matchMedia',
  'DOMRect',
] as const) {
  const value = domWindow[key as keyof Window]
  if (value === undefined) continue
  Object.defineProperty(globalThis, key, { configurable: true, value })
}

const { act } = await import('react')
const { createRoot } = await import('react-dom/client')
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'zh', resources: { zh: { translation: qyZh } } })

const { QyMyViolationCategoriesCard } =
  await import('../components/categories-card')
const { qyEnforcementActionKey } =
  await import('../../../lib/violation-thresholds')

/** 处置动作的文案由被测代码同一个 helper 决定，不抄字面量。 */
const ACTION = () => i18next.t(qyEnforcementActionKey('ban'))

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

let mounted: {
  container: HTMLDivElement
  root: ReturnType<typeof createRoot>
} | null = null

async function unmount() {
  if (mounted == null) return
  const current = mounted
  mounted = null
  await act(async () => current.root.unmount())
  current.container.remove()
}

after(unmount)

type Card = Parameters<typeof QyMyViolationCategoriesCard>[0]['data']

async function render(data: Card): Promise<string> {
  await unmount()
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  await act(async () => {
    root.render(<QyMyViolationCategoriesCard data={data} />)
  })
  mounted = { container, root }
  return container.textContent ?? ''
}

const BASE = {
  items: [],
  account_threshold: 10,
  account_hit_count: 9,
  account_window_hours: 24,
  policy_action: 'ban',
  banned: false,
  threshold_semantics: 'any_line',
} satisfies NonNullable<Card>

describe('一个类型都没公示时', () => {
  test('账号总量线仍然显示 —— 它是这种配置下唯一的封号倒计时', async () => {
    const text = await render(BASE)

    const expected = i18next.t('qy_vio_cat_account_line', {
      hit: 9,
      threshold: 10,
      hours: 24,
      action: ACTION(),
    })
    assert.ok(
      text.includes(expected),
      `账号总量线整段消失了。未公示的类型照样计数、照样封人，` +
        `而三块统计已经移除，这一页上再没有任何倒计时。实际渲染：${text}`
    )
  })

  test('顺带说明「未公示的类型照样计数」，免得用户以为站上没有别的门槛', async () => {
    const text = await render(BASE)
    assert.ok(text.includes(i18next.t('qy_vio_cat_none_published_note')))
  })

  test('账号线也没设门槛时才整块收起 —— 那时确实一句有效信息都没有', async () => {
    const text = await render({ ...BASE, account_threshold: 0 })
    assert.equal(text, '', `实际渲染：${text}`)
  })

  test('有公示类型时照旧，逐类那几句一条不少', async () => {
    const text = await render({
      ...BASE,
      items: [
        {
          id: 1,

          title: '垃圾内容',
          description: '',
          hit_count: 2,
          threshold: 5,
          window_hours: 24,
          remaining: 3,
        },
      ],
    } as NonNullable<Card>)

    assert.ok(
      text.includes(
        i18next.t('qy_vio_cat_sentence', {
          title: '垃圾内容',
          hit: 2,
          threshold: 5,
          hours: 24,
          action: ACTION(),
        })
      ),
      `逐类那一句不见了。实际渲染：${text}`
    )
    assert.ok(
      text.includes(i18next.t('qy_vio_cat_any_line_note')),
      '两条线是 OR 的那句说明必须还在'
    )
    assert.ok(
      !text.includes(i18next.t('qy_vio_cat_none_published_note')),
      '有公示类型时不该再说「本站未公示具体的违规类型」'
    )
  })

  test('data 为空时整块不渲染', async () => {
    assert.equal(await render(undefined), '')
  })
})
