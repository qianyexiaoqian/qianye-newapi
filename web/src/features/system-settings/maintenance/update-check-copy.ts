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
import type { QyUpdateCheck } from '@/features/qy/pages/admin-health/types'

/**
 * 二开检查更新的每一种结局对应的文案键。
 *
 * # 为什么是一张表,而不是组件里的一串 switch
 *
 * 类型写成 `Record<QyUpdateCheck['status'], string>` 之后,**穷尽性由
 * TypeScript 保证**:后端新增一种 status 而前端漏了,typecheck 直接红。
 * 写成 switch 的话漏掉的分支会 fall through 到 undefined,界面上渲染成一片
 * 空白 —— 而空白与"检查通过、没事"在视觉上完全一样,是最坏的失败形态。
 *
 * # 为什么单独成模块
 *
 * 组件文件是 .tsx,导入它会连带拉进 React、react-query、sonner 与整套 UI
 * 组件;而这张表要被一条纯 node:test 用例逐键核对(每个键在 zh/en 里都存在、
 * 六种结局不许共用同一句话)。把常量留在组件里,那条用例就只能靠 grep 源码
 * 来做,而 grep 挡不住"键写错了但两边都错成同一个样子"。
 */
export const QY_UPDATE_STATUS_I18N: Record<QyUpdateCheck['status'], string> = {
  update_available: 'qy_upd_update_available',
  up_to_date: 'qy_upd_up_to_date',
  ahead: 'qy_upd_ahead',
  no_release: 'qy_upd_no_release',
  current_unknown: 'qy_upd_current_unknown',
  latest_unparsable: 'qy_upd_latest_unparsable',
}

/**
 * 后端 `qianye/controller/update_check.go` 的六个失败 code。
 *
 * 这份清单存在的唯一理由是让 `update-check-copy.test.ts` 能反过来核对
 * `QY_ERROR_CODE_I18N` 里六条全都登记了、且映射到六句**不同**的话。
 * 漏登记的表现极其隐蔽:五个 502 会一起塌成"服务器出错了",一个 429 塌成
 * "请求过于频繁",后端把失败拆成六档的那一层工作在最后一米被抹平,
 * 而管理员要做的下一步四者完全不同(查出网 / 查仓库地址 / 等一小时 /
 * 查出站 IP 是不是被封)。
 */
export const QY_UPDATE_ERROR_CODES = [
  'qy_update_unreachable',
  'qy_update_rate_limited',
  'qy_update_source_missing',
  'qy_update_forbidden',
  'qy_update_unexpected_status',
  'qy_update_bad_payload',
] as const
