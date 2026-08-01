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
import { queryOptions } from '@tanstack/react-query'

import { qyGet, qyPost, qyPut } from './api'
import { qyKeys } from './query-keys'

/**
 * 支付密码的类型、接口与共享常量。
 *
 * 放在 `lib/` 而不是某个页面目录里，是因为它有两个消费方：
 *   - `pages/pay-password` 的管理页（设置 / 修改 / 邮箱找回）；
 *   - `components/qy-pay-password-field` —— 划转执行入口那一格输入框。
 * 放进任一页面目录都会让另一方跨页面 import，那正是拷贝开始漂移的地方。
 *
 * 后端契约见 `qianye/modules/paypass/`。
 */

/** 支付密码状态。字段与后端 `statusView` 一一对应。 */
export type QyPayPasswordStatus = {
  is_set: boolean
  locked: boolean
  /** 锁定截止的 unix 秒；未锁定时为 0。 */
  locked_until: number
  fail_count: number
  remaining_attempts: number
  max_attempts: number
  lock_minutes: number
  set_at: number
  changed_at: number
}

/**
 * 强度边界，与后端 `qianye/modules/paypass/hash.go` 的
 * `minPasswordBytes` / `maxPasswordBytes` 对齐。
 *
 * 前端这份只用来给出即时反馈，**不是**校验的依据 —— 真正说了算的是后端。
 * 上界按字节而不是字符：bcrypt 在 72 字节处静默截断，后端因此按字节卡。
 */
export const QY_PAY_PWD_MIN_BYTES = 6
export const QY_PAY_PWD_MAX_BYTES = 64

/** 按 UTF-8 字节数计算长度，与后端 `len(password)` 同口径。 */
export function qyPayPwdByteLength(value: string): number {
  return new TextEncoder().encode(value).length
}

/**
 * 支付密码状态查询。
 *
 * `staleTime: 0`：锁定状态与剩余次数会随用户自己的输入变化，缓存住会让
 * 用户在已经被锁定之后还看到"还剩 3 次"。
 */
export function qyPayPasswordStatusQuery() {
  return queryOptions({
    queryKey: qyKeys.payPassword(),
    queryFn: () => qyGet<QyPayPasswordStatus>('/pay-password'),
    staleTime: 0,
  })
}

export function qySetPayPassword(body: { password: string }) {
  return qyPost<{ is_set: boolean }>('/pay-password', body)
}

export function qyChangePayPassword(body: {
  old_password: string
  password: string
}) {
  return qyPut<{ is_set: boolean }>('/pay-password', body)
}

/** 往已绑定的邮箱发找回验证码。未绑定邮箱时后端返回 `qy_pay_pwd_email_unbound`。 */
export function qySendPayPasswordRecoverCode() {
  return qyPost<{ email_masked: string; expires_in_minutes: number }>(
    '/pay-password/recover/code'
  )
}

export function qyResetPayPasswordByEmail(body: {
  code: string
  password: string
}) {
  return qyPost<{ is_set: boolean }>('/pay-password/recover/reset', body)
}
