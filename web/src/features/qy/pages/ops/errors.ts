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
import type { TFunction } from 'i18next'

import { isQyError, qyErrorMessage } from '../../lib/api'

/** 后端 guard 层的 404 code，只有这两个才真正表示「功能被关掉」。 */
const GUARD_HIDE_CODES = new Set(['qy_disabled', 'qy_feature_off'])

/**
 * 运维页面的错误文案。
 *
 * 在共享的 {@link qyErrorMessage} 之上修两处只在管理端才暴露的偏差：
 *
 *  1. **带业务 code 的 404 不是「功能未启用」**。共享层把所有 404 归成
 *     `disabled`（那是为了让未注册路由静默隐藏入口），但违规记录、资金单、
 *     申诉的「不存在」也返回 404，只是额外带了 `qy_vio_not_found` /
 *     `qy_order_not_found`。照共享层渲染会告诉管理员「功能未启用」，
 *     而实际上只是单据 id 写错了。
 *  2. **400 的后端原文必须透出**。违规规则保存是逐字段校验的
 *     （`ValidateRule`），`fee_mode 非 none 时 action 必须含 charge` 这类
 *     信息是管理员唯一的修正依据；退回成通用的「请求无效」等于让人瞎猜。
 *     代价是这段文案是后端硬编码中文、不随语言切换 —— 对只有管理员能看到的
 *     诊断信息，可诊断性优先于可翻译性。
 */
export function qyOpsErrorMessage(error: unknown, t: TFunction): string {
  if (!isQyError(error)) return qyErrorMessage(error, t)

  if (
    error.status === 404 &&
    error.code != null &&
    !GUARD_HIDE_CODES.has(error.code)
  ) {
    return error.rawMessage ?? t('qy_err_not_found')
  }
  if (
    (error.kind === 'invalid' || error.kind === 'business') &&
    error.rawMessage != null
  ) {
    return error.rawMessage
  }
  return qyErrorMessage(error, t)
}

/**
 * 该错误是否应当被当作「功能未启用」静默隐藏。
 *
 * 与 `QyError.isHidden` 的差别同上：带业务 code 的 404 不算。页面的空态
 * 判定必须用这个，否则查一条不存在的记录会把整页变成「功能未启用」。
 */
export function isQyOpsHidden(error: unknown): boolean {
  if (!isQyError(error)) return false
  if (error.status === 404 && error.code != null) {
    return GUARD_HIDE_CODES.has(error.code)
  }
  return error.isHidden
}
