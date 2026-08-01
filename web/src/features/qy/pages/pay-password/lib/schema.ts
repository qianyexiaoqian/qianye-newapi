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
import { z } from 'zod'

import {
  QY_PAY_PWD_MAX_BYTES,
  QY_PAY_PWD_MIN_BYTES,
  qyPayPwdByteLength,
} from '../../../lib/pay-password'

/**
 * 新支付密码的前端校验。
 *
 * # 它与后端的关系
 *
 * 这份校验**只用来即时提示**，说了算的永远是后端 `validateStrength`。
 * 两处规则写成一样是为了让用户在提交前就看到问题，而不是为了替代后端 ——
 * 后端那份必须独立成立，因为接口可以被直接调用。
 *
 * 规则与 `qianye/modules/paypass/hash.go` 逐条对齐：
 *   1. UTF-8 字节长度落在 [6, 64]（bcrypt 在 72 字节处静默截断，所以按字节卡）
 *   2. 不得从头到尾同一个字符
 *   3. 纯数字时不得连续递增/递减
 */
export const qyPayPasswordSchema = z
  .string()
  .refine(
    (v) =>
      qyPayPwdByteLength(v) >= QY_PAY_PWD_MIN_BYTES &&
      qyPayPwdByteLength(v) <= QY_PAY_PWD_MAX_BYTES,
    'qy_pp_err_length'
  )
  .refine((v) => !isSameRun(v), 'qy_pp_err_too_simple')
  .refine(
    (v) => !isAllDigits(v) || !isConsecutiveRun(v),
    'qy_pp_err_too_simple'
  )

function isAllDigits(s: string): boolean {
  return s.length > 0 && /^[0-9]+$/.test(s)
}

function isSameRun(s: string): boolean {
  return s.length > 0 && [...s].every((ch) => ch === s[0])
}

function isConsecutiveRun(s: string): boolean {
  if (s.length < 2) return false
  let up = true
  let down = true
  for (let i = 1; i < s.length; i += 1) {
    if (s.charCodeAt(i) !== s.charCodeAt(i - 1) + 1) up = false
    if (s.charCodeAt(i) !== s.charCodeAt(i - 1) - 1) down = false
  }
  return up || down
}
