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
import type { QyLotBound } from '../types'

/**
 * 一个取值是否落在后端下发的区间里。
 *
 * 后端对**没有上界**的键不下发 `max`（`settingBound.NoMax`，见
 * `qianye/modules/lottery/api_admin_config.go`）。这个判定必须单独成一个函数，
 * 因为写成裸的 `value > bound.max` 时，`max` 缺席那一档会得到
 * `value > undefined` —— 恒为 `false`，于是"碰巧也放行了"。
 *
 * 碰巧对不是对：把 `max` 改成一个哨兵数字、或者后端某天补上 `max` 之后，
 * 那份"碰巧"会一起翻面，而翻面的表现是运营在一格输入框里看到一个红框却找不到
 * 任何超限提示。所以缺席这件事在这里被显式写出来。
 */
export function qyLotBoundContains(bound: QyLotBound, value: number): boolean {
  if (value < bound.min) return false
  return bound.max == null || value <= bound.max
}

/**
 * 「0 = 不限制」的那几个额度上限（键名与后端 `editableKeys` / `yaml_readonly`
 * 逐字一致）。
 *
 * 逐条列出而不是按 `_quota` 后缀猜：`pay_password_threshold_quota` 也以 `_quota`
 * 结尾，而它的 0 是「任何金额都不验支付密码」，`large_prize_alert_quota` 的 0 是
 * 「连确认都不要」—— 三种 0 的意思互不相同，一句话说不完。
 */
const UNLIMITED_WHEN_ZERO = new Set([
  'max_stake_quota',
  'max_total_prize_quota',
])

/**
 * 这个键的这个**取值**是不是「不限制」。
 *
 * 只读渲染必须据此说「不限」而不是 `$0`：一行写着「单场奖品总额上限 $0」的
 * 只读文字，任何人读到的都是"一分钱都不许发"，而真实语义恰好相反 ——
 * 这与项目方那句「怎么在抽奖设置这里不能超过 100 站点余额」是同一种误读，
 * 只是方向相反。
 *
 * **区间端点不适用**：区间的下界 0 是一个边界而不是取值，套上这层语义会得到
 * 「可填范围：不小于 不限」。
 */
export function qyLotIsUnlimitedZero(key: string, value: number): boolean {
  return value === 0 && UNLIMITED_WHEN_ZERO.has(key)
}
