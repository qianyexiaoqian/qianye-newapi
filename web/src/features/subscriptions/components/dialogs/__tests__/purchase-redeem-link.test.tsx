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
 * 购买弹窗里的「购买兑换码」入口：套餐配了站外买码地址就多一种付款方式，
 * 点了在新标签页打开那个地址；地址不合法时整块不出现；发售时间窗已关时与其它
 * 付款按钮一起禁用（兑换时后端照样会拒，别让用户先在站外付了钱）。
 */
import assert from 'node:assert/strict'
import { after, test } from 'node:test'

import { Window } from 'happy-dom'

const domWindow = new Window({ width: 1280, height: 900 })
for (const key of [
  'window',
  'document',
  'navigator',
  'HTMLElement',
  'Node',
  'Element',
  'Event',
  'MouseEvent',
  'CustomEvent',
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
const { QueryClient, QueryClientProvider } = await import(
  '@tanstack/react-query'
)
const i18next = (await import('i18next')).default
const { initReactI18next } = await import('react-i18next')
await i18next
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const { SubscriptionPurchaseDialog } = await import(
  '../subscription-purchase-dialog'
)
type PlanRecord = import('@/features/subscriptions/types').PlanRecord

const reactTestGlobals = globalThis as typeof globalThis & {
  IS_REACT_ACT_ENVIRONMENT?: boolean
}
reactTestGlobals.IS_REACT_ACT_ENVIRONMENT = true

const opened: Array<{ url: string; target: string; features: string }> = []
domWindow.open = ((url: string, target: string, features: string) => {
  opened.push({ url, target, features })
  return null
}) as unknown as typeof domWindow.open

let mounted: { root: ReturnType<typeof createRoot>; el: HTMLElement } | null =
  null

async function unmount() {
  if (mounted == null) return
  const current = mounted
  mounted = null
  await act(async () => current.root.unmount())
  current.el.remove()
}
after(unmount)

function planRecord(over: Partial<PlanRecord['plan']>): PlanRecord {
  return {
    plan: {
      id: 3,
      title: '哈基米套餐',
      price_amount: 10,
      currency: 'USD',
      duration_unit: 'month',
      duration_value: 1,
      quota_reset_period: 'never',
      enabled: true,
      sort_order: 0,
      allow_balance_pay: true,
      allow_wallet_overflow: true,
      max_purchase_per_user: 0,
      total_amount: 0,
      no_quota: false,
      ...over,
    },
  }
}

async function mountDialog(
  plan: PlanRecord,
  onOpenChange: (open: boolean) => void = () => {}
) {
  await unmount()
  opened.length = 0
  const el = document.createElement('div')
  document.body.appendChild(el)
  const root = createRoot(el)
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <SubscriptionPurchaseDialog
          open
          onOpenChange={onOpenChange}
          plan={plan}
          userQuota={0}
        />
      </QueryClientProvider>
    )
  })
  mounted = { root, el }
}

function redeemButton(): HTMLButtonElement | null {
  const buttons = [...document.body.querySelectorAll('button')]
  return (buttons.find((b) =>
    b.textContent?.includes('qy_plan_redeem_buy_action')
  ) ?? null) as HTMLButtonElement | null
}

test('plan with a redeem link: clicking opens it in a new tab and closes the dialog', async () => {
  const closed: boolean[] = []
  await mountDialog(
    planRecord({ redeem_purchase_link: 'https://shop.example/item/9' }),
    (open) => closed.push(open)
  )
  const button = redeemButton()
  assert.ok(button, '配了买码地址却没有「购买兑换码」入口')
  assert.equal(button.disabled, false)

  await act(async () => button.click())
  assert.deepEqual(opened, [
    {
      url: 'https://shop.example/item/9',
      target: '_blank',
      features: 'noopener,noreferrer',
    },
  ])
  assert.deepEqual(closed, [false], '打开后应关掉弹窗，让用户回到钱包页的兑换框')
})

test('plan without a redeem link shows no redeem entry', async () => {
  await mountDialog(planRecord({ redeem_purchase_link: '' }))
  assert.equal(redeemButton(), null)
})

test('a non-http redeem link stored in the database is never rendered as a link', async () => {
  await mountDialog(planRecord({ redeem_purchase_link: 'javascript:alert(1)' }))
  assert.equal(redeemButton(), null)
})

test('redeem entry is disabled once the sale window has ended', async () => {
  await mountDialog(
    planRecord({
      redeem_purchase_link: 'https://shop.example/item/9',
      sale_end_at: 1_000_000_000,
    })
  )
  const button = redeemButton()
  assert.ok(button)
  assert.equal(button.disabled, true)
})

test('a separate balance price is shown and is what the balance button charges', async () => {
  await mountDialog(planRecord({ price_amount: 10, balance_price_amount: 0.5 }))
  const text = document.body.textContent ?? ''
  assert.ok(text.includes('qy_plan_balance_price'), '余额购买价那一行没出现')
  assert.ok(text.includes('$0.50'), `余额购买价没印出来：${text}`)
})

test('without a separate balance price no extra price row appears', async () => {
  await mountDialog(planRecord({ price_amount: 10, balance_price_amount: null }))
  const text = document.body.textContent ?? ''
  assert.ok(!text.includes('qy_plan_balance_price'))
})
