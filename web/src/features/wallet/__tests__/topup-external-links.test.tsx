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
 * 「站外跳转」在充值卡里的三条契约。
 *
 * 1. **跟着金额走**。每条外链绑一个充值金额，选中那个金额它才出现；金额上没绑
 *    东西时那一格只剩支付通道 —— 这正是"如果没有设置那就只能使用付款方式"。
 * 2. **没有任何支付通道时它照样能被选到**。这条外链存在的全部理由就是"本站不接
 *    支付、兑换码在站外卖"，而这恰好是 hasConfigurableTopup 为假的那一档：
 *    金额格子必须照常渲染，否则用户根本没有办法选中那个金额、把链接翻出来。
 * 3. **地址只放行 http(s)**。这个值一路从管理员的输入框走到 <a href>，
 *    javascript: 落到那里就是一次存储型 XSS。后端也过滤，这里是第二道。
 */
import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'

import type { PresetAmount, TopupInfo } from '../types'

const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { RechargeFormCard } = await import('../components/recharge-form-card')
const { externalLinksForAmount, usableExternalLinks } =
  await import('../lib/payment')

const i18n = createInstance()
await i18n
  .use(initReactI18next)
  .init({ lng: 'en', resources: { en: { translation: {} } } })

const PRESETS: PresetAmount[] = [{ value: 50 }, { value: 100 }]

const NO_GATEWAY: TopupInfo = {
  enable_online_topup: false,
  enable_stripe_topup: false,
  pay_methods: [],
  min_topup: 1,
  stripe_min_topup: 1,
  amount_options: [50, 100],
  discount: {},
  enable_redemption: true,
  external_links: [
    { amount: 50, name: 'Buy a $50 code', url: 'https://shop.example.com/50' },
    {
      amount: 100,
      name: 'Buy a $100 code',
      url: 'https://shop.example.com/100',
    },
  ],
}

function renderCard(topupInfo: TopupInfo, topupAmount: number) {
  return render(
    <I18nextProvider i18n={i18n}>
      <RechargeFormCard
        topupInfo={topupInfo}
        presetAmounts={PRESETS}
        selectedPreset={topupAmount}
        onSelectPreset={() => {}}
        topupAmount={topupAmount}
        onTopupAmountChange={() => {}}
        paymentAmount={topupAmount}
        calculating={false}
        onPaymentMethodSelect={() => {}}
        paymentLoading={null}
        redemptionCode=''
        onRedemptionCodeChange={() => {}}
        onRedeem={() => {}}
        redeeming={false}
      />
    </I18nextProvider>
  )
}

const NO_METHODS_ALERT =
  'No payment methods available. Please contact administrator.'

describe('external topup links in the recharge card', () => {
  test('shows only the link bound to the selected amount', () => {
    renderCard(NO_GATEWAY, 50)

    const fifty = screen.getByRole('link', { name: /Buy a \$50 code/ })
    expect(fifty).toHaveAttribute('href', 'https://shop.example.com/50')
    expect(fifty).toHaveAttribute('target', '_blank')
    expect(fifty).toHaveAttribute('rel', 'noopener noreferrer')
    expect(screen.queryByRole('link', { name: /Buy a \$100 code/ })).toBeNull()
  })

  test('keeps the amount tiles selectable when no gateway is configured', () => {
    renderCard(NO_GATEWAY, 50)

    expect(screen.getByRole('button', { name: /50/ })).toBeInTheDocument()
    expect(screen.queryByText(NO_METHODS_ALERT)).toBeNull()
  })

  test('falls back to payment methods only on an amount with nothing bound', () => {
    renderCard({ ...NO_GATEWAY, enable_online_topup: true }, 20)

    expect(screen.queryByRole('link', { name: /Buy a \$50 code/ })).toBeNull()
    expect(screen.getByText(NO_METHODS_ALERT)).toBeInTheDocument()
  })
})

describe('externalLinksForAmount', () => {
  test('matches a hand-typed amount the same as a preset tile', () => {
    expect(externalLinksForAmount(NO_GATEWAY.external_links, 100)).toEqual([
      {
        amount: 100,
        name: 'Buy a $100 code',
        url: 'https://shop.example.com/100',
      },
    ])
    expect(externalLinksForAmount(NO_GATEWAY.external_links, 20)).toEqual([])
  })
})

describe('usableExternalLinks', () => {
  test('drops entries that must never reach an href', () => {
    expect(
      usableExternalLinks([
        { amount: 50, name: 'ok', url: 'https://shop.example.com/50' },
        { amount: 50, name: 'lan', url: 'http://shop.internal:8080/buy' },
        { amount: 50, name: 'xss', url: 'javascript:alert(1)' },
        { amount: 50, name: 'data', url: 'data:text/html,<script></script>' },
        { amount: 50, name: 'relative', url: '/shop/item/50' },
        { amount: 50, name: '   ', url: 'https://shop.example.com/100' },
        { amount: 50, name: 'blank', url: '  ' },
        { amount: 0, name: 'unbound', url: 'https://shop.example.com/x' },
      ])
    ).toEqual([
      { amount: 50, name: 'ok', url: 'https://shop.example.com/50' },
      { amount: 50, name: 'lan', url: 'http://shop.internal:8080/buy' },
    ])
  })

  test('treats a missing list as no links', () => {
    expect(usableExternalLinks(undefined)).toEqual([])
  })
})
