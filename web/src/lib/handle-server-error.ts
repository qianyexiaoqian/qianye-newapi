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
import { AxiosError } from 'axios'
import i18next from 'i18next'
import { toast } from 'sonner'

import { getServerErrorMessageKey } from '@/lib/server-error-message'

export function handleServerError(error: unknown) {
  // eslint-disable-next-line no-console
  console.log(error)

  let errMsg = i18next.t('Something went wrong!')

  const messageKey = getServerErrorMessageKey(error)
  if (messageKey) {
    toast.error(i18next.t(messageKey))
    return
  }

  if (
    error &&
    typeof error === 'object' &&
    'status' in error &&
    Number(error.status) === 204
  ) {
    errMsg = i18next.t('Content not found.')
  }

  if (error instanceof AxiosError) {
    // 取第一个**非空字符串**,不要无条件覆盖 errMsg。
    //
    // 原来这里是 `errMsg = error.response?.data.title` —— 而本仓后端的错误体
    // 恒为 `{success:false, message:"..."}`(common/gin.go 的 ApiError),
    // 根本没有 title 这一栏。于是 errMsg 变成 undefined,`toast.error(undefined)`
    // 弹出一个**空白提示**:用户只看到一个没有字的红条,既不知道哪儿错了,
    // 也不知道要不要重试。凡是走到这个兜底的失败操作都是这个样子。
    const body = error.response?.data as
      | { message?: unknown; title?: unknown }
      | undefined
    const candidates = [body?.message, body?.title, error.message]
    const resolved = candidates.find(
      (value) => typeof value === 'string' && value.trim() !== ''
    )
    if (typeof resolved === 'string') {
      errMsg = resolved
    }
  }

  toast.error(errMsg)
}
