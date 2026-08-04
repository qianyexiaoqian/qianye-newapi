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
import type { QyViolationRule } from '../types'

/** 行内开关待确认的一次切换。`next` 显式带上，不从 `rule.enabled` 反推。 */
export type QyPendingToggle = { rule: QyViolationRule; next: boolean }

/**
 * 这次启停要不要二次确认。
 *
 * 项目方要的是「快速启用关闭」，所以默认不拦 —— 每一次点击都弹框，就退化成一个
 * 比编辑抽屉快不了多少的东西。只拦两种真正会造成损失的方向：
 *
 *  1. **任何停用。** 关掉一条防护规则**没有任何症状**：接口照常 200、业务照常跑，
 *     只是从此零命中。误关一个业务开关几分钟内就有人来投诉；误关一条防护规则
 *     可以安静地躺几个月，而发现它的方式通常是「怎么一条都没抓到」—— 这正是本仓库
 *     反复出现的那个形状（内置规则包从没导入过，单测却 25/31 全绿）。
 *     代价不对称，所以确认成本也不该对称。
 *  2. **启用一条真实模式（enforce）的规则。** 它下一秒就开始真的扣费、阻断、
 *     累计封号。这是这一页最重的一个动作，不该由一次手滑触发。
 *
 * 反过来，**启用一条影子规则不需要确认**：影子只记录，不扣钱、不阻断、不计数，
 * 而它恰恰是导入内置规则包之后最高频的动作 —— 那正是「快速」这个词要服务的场景。
 *
 * 这里刻意不上「不可逆」那一档（不强制勾选确认框）：启停完全可逆，一次点击就能
 * 改回来。把成本加在一个能撤销的动作上，只会训练人闭着眼睛勾。
 */
export function qyToggleNeedsConfirm(
  rule: QyViolationRule,
  next: boolean
): boolean {
  return !next || rule.mode === 'enforce'
}
