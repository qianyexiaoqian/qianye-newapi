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

import { api } from '@/lib/api'

import {
  QY_API_PREFIX,
  qyDelete,
  qyErrorFromBlobFailure,
  qyGet,
  qyPost,
} from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import type { QyPage } from '../../lib/types'
import type {
  QyPayeeAccount,
  QyWithdrawConfig,
  QyWithdrawCreateRequest,
  QyWithdrawProof,
  QyWithdrawal,
} from './types'

/**
 * 提现门槛 + 当前用户可提额度。
 *
 * `staleTime: 0`：`withdrawable_quota` / `used_today` 会随用户自己的提交变化，
 * 缓存住会让"刚提完还显示能提"。
 */
export function qyWithdrawConfigQuery() {
  return queryOptions({
    queryKey: qyKeys.withdrawConfig(),
    queryFn: () => qyGet<QyWithdrawConfig>('/withdraw/config'),
    staleTime: 0,
  })
}

export function qyWithdrawPayeesQuery() {
  return queryOptions({
    queryKey: qyKeys.withdrawPayees(),
    queryFn: async () => {
      const page = await qyGet<{ items: QyPayeeAccount[] }>('/withdraw/payees')
      return page.items ?? []
    },
  })
}

export function qyWithdrawRecordsQuery(params: {
  p: number
  page_size: number
  status?: string
  method?: string
}) {
  const query: Record<string, unknown> = {
    p: params.p,
    page_size: params.page_size,
  }
  // 空串不进 query：axios 会把 `status=` 原样拼进 URL，而 queryKey 里多一个
  // 恒空的字段会让同一组筛选条件分裂成两个缓存条目。
  if (params.status != null && params.status !== '') {
    query.status = params.status
  }
  if (params.method != null && params.method !== '') {
    query.method = params.method
  }

  return queryOptions({
    queryKey: qyKeys.withdrawRecords(query),
    queryFn: () => qyGet<QyPage<QyWithdrawal>>('/withdraw/records', query),
  })
}

/** 单据详情。**只有这个接口会带 `events`**，时间线必须走它而不是列表行。 */
export function qyWithdrawRecordQuery(id: number) {
  return queryOptions({
    queryKey: qyKeys.withdrawRecord(id),
    queryFn: () => qyGet<QyWithdrawal>(`/withdraw/${id}`),
  })
}

export function qyCreateWithdrawal(body: QyWithdrawCreateRequest) {
  return qyPost<QyWithdrawal>('/withdraw', body)
}

export function qyCancelWithdrawal(id: number) {
  return qyPost<QyWithdrawal>(`/withdraw/${id}/cancel`)
}

export function qyCreatePayee(body: {
  channel: string
  label: string
  payee: Record<string, string>
}) {
  return qyPost<QyPayeeAccount>('/withdraw/payees', body)
}

export function qyDeletePayee(ref: string) {
  return qyDelete<{ ref: string }>(`/withdraw/payees/${ref}`)
}

/**
 * 上传一张提现凭证，返回可用于 `proof_ref` 的标识。
 *
 * 表单字段名**必须是 `file`**：后端 `acceptProofUpload` 读的是 `c.FormFile("file")`，
 * 换个名字会得到 `qy_wd_proof_required`（"请选择要上传的图片"），而用户明明选了。
 *
 * 刻意不手动设置 `Content-Type`：multipart 需要一个带 boundary 的头，写死
 * `multipart/form-data` 反而会让服务端解析不出任何字段。交给浏览器生成。
 *
 * 这是两步式的第一步。图片本体从不随申请请求走 —— 申请那一步跑在会冻结佣金的
 * 事务里，把几 MiB 的上行塞进去等于让事务持有时间跟着用户带宽走。
 */
export function qyUploadWithdrawProof(file: File) {
  const form = new FormData()
  form.append('file', file)
  return qyPost<QyWithdrawProof>('/withdraw/proofs', form)
}

/**
 * 取回某张单的凭证图片本体。
 *
 * **走鉴权接口而不是直链**：图片在磁盘上，没有任何静态路由能到达它；越权判定在
 * 后端 `loadUserWithdrawal`（`user_id` 进 WHERE）。返回 Blob 交给调用方
 * `createObjectURL` —— 调用方必须在卸载时 `revokeObjectURL`，这是 PII。
 *
 * 不能用 `qyGet`：那条路会把响应体当 `{success,data}` 信封解，而这里的成功响应
 * 就是二进制图片本身。失败时 axios 给回的 `response.data` 也是 Blob（responseType
 * 对错误响应一视同仁），所以错误还原走 `qyErrorFromBlobFailure`，否则 410
 * `qy_wd_proof_purged`（"已按保留期清理"）会被糊成一句"请求参数不合法"。
 *
 * `admin=true` 走管理端那条路径（按单据 id 作用域，越权判定在
 * `loadDecidableWithdrawal` 之外的读取口上）。两条路径的响应形状逐字相同，
 * 所以共用这一个函数 —— 抄第二份出来的下场是错误还原只修在其中一边。
 */
export async function qyFetchWithdrawProofBlob(
  id: number,
  admin = false
): Promise<Blob> {
  const path = admin
    ? `${QY_API_PREFIX}/admin/withdraw/${id}/proof`
    : `${QY_API_PREFIX}/withdraw/${id}/proof`
  try {
    const res = await api.get(path, {
      skipErrorHandler: true,
      skipBusinessError: true,
      responseType: 'blob',
      // 上游的在途 GET 去重只按 url + params 归并，认不出 responseType 的差异。
      disableDuplicate: true,
    })
    return res.data as Blob
  } catch (error) {
    throw await qyErrorFromBlobFailure(error)
  }
}
