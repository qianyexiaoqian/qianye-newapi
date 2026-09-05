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

import { isQyError, qyErrorMessage } from '../../../lib/api'

/**
 * 「后端那句话就是答案」的两个 code（`qianye/modules/stardust/errors.go`）。
 *
 * `qy_sd_bad_setting` 一个 code 背后是 `saveOverrides` 的六条判据（单位名太长 /
 * 不是整数 / 超出 [lo, hi] / 不可在线改的键…），每一条都写着是哪一项、区间是多少；
 * `qy_sd_bad_request` 同理（手调的「事由至少 4 个字符」「delta 不能为 0」、
 * 重跑的「该日尚未封口」）。走共享层会把它们塌成一句「请求参数有误」，
 * 运营只能去猜是哪一格。
 *
 * 刻意不进 `lib/api.ts` 的 `QY_SERVER_MESSAGE_CODES`：那张表要求回落文案已在
 * 主语言包里，而本轮的文案只落在片段文件，合并之前会把那条守卫弄红。
 * 客户端校验已经把绝大多数情形拦在提交之前，走到这里的是漏网的那几条。
 */
const QY_SD_RAW_MESSAGE_CODES: ReadonlySet<string> = new Set([
  'qy_sd_bad_setting',
  'qy_sd_bad_request',
])

/** 星屑管理端的错误文案：两个"整族共用"的 code 透出后端原文，其余走共享层。 */
export function qySdAdminErrorMessage(error: unknown, t: TFunction): string {
  if (
    isQyError(error) &&
    error.code != null &&
    QY_SD_RAW_MESSAGE_CODES.has(error.code) &&
    error.rawMessage != null &&
    error.rawMessage !== ''
  ) {
    return error.rawMessage
  }
  return qyErrorMessage(error, t)
}
