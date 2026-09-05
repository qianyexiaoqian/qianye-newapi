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
/**
 * 星辉佣金页面（D-15）的 happy-dom 渲染夹具。
 *
 * 集成前它在 `stardust-screen.tsx` 之上叠一份片段语言包
 * （`pending-commission.zh.json`）：新键还没并进主包，不叠的话下面的中文断言
 * 只能对着裸键写。叠加走 i18next 自己的 `addResourceBundle`（非 deep：整包替换），
 * 在每次挂载**之前**补一次：`mountQyStardustScreen` 随后再把主包并进去时只覆盖
 * 主包里有的键，片段那几十个键原样留下；别的测试文件重新 init 过 i18next 也
 * 一样能补回来。**绝不**就地改 `stardust-screen` 导出的那个主包对象 —— 同一进程
 * 里别的测试拿它与 `en.json` 比键数，改了它就是给隔壁的守卫造一条假失败。
 *
 * 主编排把片段并进 `zh.json` 之后，本文件退化成别名（与 `invite-screen.ts` 同形）。
 */

import i18next from 'i18next'

import { mountQyStardustScreen, zh } from './stardust-screen'

/** 主语言包(星辉佣金的文案已并入 zh.json,集成前曾叠过片段)。断言从这里取中文文案,不从产品代码回读。 */
export const commissionZh: Record<string, string> = zh

/**
 * 整屏不许出现的四个词。它们是项目方点名要规避的东西（「不再直接结算成现金，
 * 规避非法集资、拉人头等闲话」），一个字都不能留在任何一张佣金相关的屏幕上。
 */
export const QY_COMMISSION_FORBIDDEN_WORDS = [
  '提现',
  '打款',
  '现金',
  '元',
] as const

export function mountQyCommissionScreen(
  options: Parameters<typeof mountQyStardustScreen>[0]
): ReturnType<typeof mountQyStardustScreen> {
  // deep=false：i18next 会用 `{ ...现有包, ...这份 }` 换掉整个包，而不是把片段
  // **就地合并进**现有那个对象。现有那个对象可能正是别的测试文件 init 时交进去的
  // `zh.json` 模块本身 —— 就地合并等于给全进程的 `zh.json` 多塞几十个键，隔壁
  // 拿它与 `en.json` 比键数的守卫会当场变红。
  i18next.addResourceBundle('zh', 'translation', commissionZh, false, true)
  return mountQyStardustScreen(options)
}

export {
  ROLE,
  act,
  cleanupQyStardustScreens,
  setQyProbeRole,
  type QyProbeRequest,
} from './stardust-screen'
