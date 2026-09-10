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
import { describe, expect, test } from 'vitest'

import { pickTwoFALoginChallenge } from '../two-fa-challenge'

// 闸门在后端的 setupLogin 上,密码、OAuth 回调、微信、Telegram 四条通道都可能
// 收到挑战而不是会话。这里钉的是判定本身:少认一种形状,对应那条通道就会把
// 挑战当成登录失败,用户卡在"OAuth failed"上,而后端其实是对的。
describe('pickTwoFALoginChallenge', () => {
  test('require_2fa 带 flow_token 时识别为挑战', () => {
    expect(
      pickTwoFALoginChallenge({
        require_2fa: true,
        flow_token: 'flow-1',
        expires_at: 1234,
      })
    ).toEqual({ require_2fa: true, flow_token: 'flow-1', expires_at: 1234 })
  })

  test('没有 expires_at 也算挑战,该字段只用于展示', () => {
    expect(
      pickTwoFALoginChallenge({ require_2fa: true, flow_token: 'flow-2' })
    ).toEqual({
      require_2fa: true,
      flow_token: 'flow-2',
      expires_at: undefined,
    })
  })

  test('flow_token 缺失或空白时不算挑战', () => {
    expect(pickTwoFALoginChallenge({ require_2fa: true })).toBeNull()
    expect(
      pickTwoFALoginChallenge({ require_2fa: true, flow_token: '   ' })
    ).toBeNull()
  })

  test('会话应答不会被误判成挑战', () => {
    expect(
      pickTwoFALoginChallenge({ access_token: 'token', session: {} })
    ).toBeNull()
  })

  test('require_2fa 为假值时不算挑战', () => {
    expect(
      pickTwoFALoginChallenge({ require_2fa: false, flow_token: 'flow-3' })
    ).toBeNull()
    expect(
      pickTwoFALoginChallenge({ require_2fa: 'true', flow_token: 'flow-4' })
    ).toBeNull()
  })

  test('非对象输入一律不算挑战', () => {
    for (const value of [null, undefined, 'require_2fa', 0, []]) {
      expect(pickTwoFALoginChallenge(value)).toBeNull()
    }
  })
})
