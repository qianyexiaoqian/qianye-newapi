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
 * 全屏错误页的诊断信息。
 *
 * 这个模块的存在理由：`__root.tsx` 的 `errorComponent` 是全站唯一的错误边界
 * （只有 `qy/route.tsx` 另挂了一个），任何一次渲染异常、路由 chunk 加载失败、
 * loader 抛错都落在它身上，而它此前一律显示「500」——那个数字是硬编码的兜底，
 * 跟 HTTP 无关。用户看到 500 去翻服务端日志，什么都翻不到。
 *
 * 这里做两件事：把错误分类到能给出可执行建议的粒度，以及把「哪个请求、什么时候、
 * 请求 ID 是多少」拼成一行可复制的文本。
 */

/** 后端在每个响应上都挂了这个头（middleware/request-id.go）。 */
const REQUEST_ID_HEADER = 'x-oneapi-request-id'

/** 一次自动重载后的静默期，避免构建产物真的缺失时陷入刷新循环。 */
const STALE_ASSET_RELOAD_COOLDOWN_MS = 60_000

const STALE_ASSET_RELOAD_KEY = 'new-api:stale-asset-reload-at'

export interface ErrorDiagnostics {
  /** HTTP 状态码，仅当错误确实来自一次 HTTP 响应时才有。 */
  status?: number
  /** 后端请求 ID，能直接在服务端日志里搜到。 */
  requestId?: string
  /** 失败的请求，形如 `GET /api/user/self`。 */
  request?: string
  name: string
  message: string
}

function asRecord(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null
    ? (value as Record<string, unknown>)
    : undefined
}

function readHeader(headers: unknown, name: string): string | undefined {
  const record = asRecord(headers)
  if (!record) return undefined
  // axios 把响应头小写化，但 `AxiosHeaders` 实例与手写的 mock 都可能保留原样。
  for (const [key, value] of Object.entries(record)) {
    if (key.toLowerCase() !== name) continue
    if (typeof value === 'string' && value.length > 0) return value
  }
  return undefined
}

/**
 * 判断错误是不是「页面引用的构建产物取不回来」。
 *
 * 这是本仓 500 错误页最常见的来源：前端是 `go:embed` 进二进制的，重新构建后
 * 每个 chunk 的 hash 都变了，而旧标签页手里的 index.html 还指着旧文件名。
 * 用户点一个还没加载过的页面 → chunk 404 → 整棵树抛错 → 全屏 500。
 * 刷新之所以「就好了」，是因为刷新会拿到新的 index.html。
 *
 * 这类错误唯一正确的处置是重新加载页面，不是重试请求。
 */
export function isStaleAssetError(error: unknown): boolean {
  const record = asRecord(error)
  if (!record) return false
  if (record.name === 'ChunkLoadError') return true
  const message = typeof record.message === 'string' ? record.message : ''
  if (!message) return false
  return (
    /loading chunk\s+\S+\s+failed/i.test(message) ||
    /loading css chunk/i.test(message) ||
    /failed to fetch dynamically imported module/i.test(message) ||
    /error loading dynamically imported module/i.test(message) ||
    /importing a module script failed/i.test(message) ||
    // 服务端把缺失的 chunk 当成 SPA 路由回了 index.html 时，浏览器会把 HTML
    // 当 JS 解析。修好后端之后这条仍然要留着：CDN / 反代同样会这么干。
    /unexpected token '</i.test(message)
  )
}

/** 从任意错误里榨出足够定位问题的字段。拿不到的字段一律缺省，不编。 */
export function describeError(error: unknown): ErrorDiagnostics {
  const record = asRecord(error)
  const name = typeof record?.name === 'string' ? record.name : 'Error'
  const message =
    typeof record?.message === 'string' && record.message.length > 0
      ? record.message
      : String(error ?? '')

  const response = asRecord(record?.response)
  const status =
    typeof response?.status === 'number' ? response.status : undefined

  const requestId =
    readHeader(response?.headers, REQUEST_ID_HEADER) ??
    (typeof asRecord(response?.data)?.request_id === 'string'
      ? (asRecord(response?.data)?.request_id as string)
      : undefined)

  const config = asRecord(record?.config)
  const url = typeof config?.url === 'string' ? config.url : undefined
  const method = typeof config?.method === 'string' ? config.method : undefined
  const request = url ? `${(method ?? 'get').toUpperCase()} ${url}` : undefined

  return { status, requestId, request, name, message }
}

/**
 * 拼出错误页上那一行可复制的标识。
 *
 * 用户把这一行贴给运维，运维能直接拿 requestId 去 grep 服务端日志；没有
 * requestId（纯前端异常）时，至少还有路径、时间和异常名。
 */
export function formatErrorReference(
  diagnostics: ErrorDiagnostics,
  context: { path: string; at: Date }
): string {
  const parts = [context.at.toISOString(), context.path]
  if (diagnostics.request) parts.push(diagnostics.request)
  if (diagnostics.status !== undefined) parts.push(`HTTP ${diagnostics.status}`)
  if (diagnostics.requestId) parts.push(`rid=${diagnostics.requestId}`)
  parts.push(`${diagnostics.name}: ${diagnostics.message}`)
  return parts.join(' | ')
}

/**
 * 领取一次「因构建产物过期而自动重载」的额度。
 *
 * 冷却期内只给一次：如果重载之后 chunk 依然取不回来（产物真的没了，不是版本
 * 换了），第二次调用返回 false，页面停在错误页并给出手动按钮，而不是无限刷新。
 */
export function claimStaleAssetReload(
  storage: Pick<Storage, 'getItem' | 'setItem'> | undefined,
  now: number
): boolean {
  if (!storage) return false
  try {
    const previous = Number(storage.getItem(STALE_ASSET_RELOAD_KEY))
    if (
      Number.isFinite(previous) &&
      previous > 0 &&
      now - previous < STALE_ASSET_RELOAD_COOLDOWN_MS
    ) {
      return false
    }
    storage.setItem(STALE_ASSET_RELOAD_KEY, String(now))
    return true
  } catch {
    // 隐私模式下 sessionStorage 会抛。拿不到额度就不自动刷新，按钮还在。
    return false
  }
}
