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
 * 落地页里**不进语言包**的常量：品牌名与外链。
 *
 * 判据是「换一种语言之后这个字串会不会变」。`Claude` / `Cherry Studio` 在
 * 任何语种下都是同一个词（web/AGENTS.md 3.1「专有名词」），把它们塞进
 * `qy/{en,zh}.json` 只会多出两条永远相等的记录。真正的文案一律写成组件里的
 * `t('qy_home_…')` 字面量 —— 那样 `i18n-key-coverage` 的扫描器才看得见。
 */

/**
 * 页面主推的三支模型族。一处定义、三处消费：信号板的排序权重、目录为空时的
 * 兜底行、以及 §2「模型阵容」那三张票面 —— 各写一遍必然漂移成三份名单。
 * `id` 是拿去和 `model_name` 做小写包含判断的词根。
 */
export const QY_LANDING_FAMILIES = [
  { id: 'claude', label: 'Claude' },
  { id: 'gemini', label: 'Gemini' },
  { id: 'gpt', label: 'GPT' },
] as const

/** 信号板最多显示几行。再多就把首屏撑出一屏之外。 */
export const QY_LANDING_BOARD_ROWS = 7

/**
 * 开箱即用的客户端。图标一律走站内已装的 `@lobehub/icons` 或字母占位，
 * **不引外部图片**：上游首页那颗 `ccswitch.io/favicon.png` 在没有外网的
 * 部署里会走完一整轮超时才落到 onError 兜底。
 */
export const QY_LANDING_APPS = [
  { name: 'Cherry Studio', href: 'https://cherry-ai.com', short: 'CS' },
  { name: 'CC Switch', href: 'https://ccswitch.io', short: 'CC' },
  { name: 'Cline', href: 'https://cline.bot', short: 'CL' },
  { name: 'LobeChat', href: 'https://lobehub.com', short: 'LC' },
] as const
