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
 * API 基址的两条归一化规则。用户点「复制」拿到 {@link qyNormalizeApiBase} 的
 * 结果，点「带 V1 复制」拿到 {@link qyApiBaseWithV1} 的结果。
 *
 * # 为什么这件事值得单独一个文件 + 一张用例表
 *
 * 它是字符串拼接，而字符串拼接出的 `//v1` 与 `/v1/v1` 是**用户侧才会暴露**的
 * 错误：复制出去、粘进客户端、跑一段时间之后才发现请求全 404。前端没有任何
 * 东西会因此变红。
 *
 * 输入不只来自后端那张表。后端 `apiaddr.normalizeURL` 已经保证了
 * scheme ∈ {http,https}、无凭据、无查询串/片段、路径无尾斜杠，但一条都没配时
 * 用的是 {@link readSiteServerAddress} 的回落值 —— 那是运营在系统设置里手填的
 * `server_address`，或者 `window.location.origin`。前者什么都可能是。
 * 所以这里按"什么都可能进来"写。
 *
 * # 规则
 *
 * 1. 去首尾空白。
 * 2. 去掉**所有**尾部斜杠（`https://a.com///` → `https://a.com`）。
 * 3. 「带 V1」：路径部分已经以 `/v1` 段结尾就原样返回，否则追加 `/v1`。
 *
 * ## 三条刻意的判定
 *
 * · **只看路径，不看主机**。`https://v1` 的末尾三个字符恰好是 `/v1`，纯粹
 *   按后缀判会把它当成"已经带了 v1"，于是「带 V1 复制」复制出一个没有 `/v1`
 *   的地址 —— 一个静默的错。所以先把 scheme 与主机切掉再判。
 *
 * · **按段判，不按后缀判**。`https://a.com/v1beta` 不是 v1 端点，`/v1beta`
 *   也不以 `/v1` 结尾，所以会被追加成 `/v1beta/v1`。这个结果看起来奇怪，但
 *   它奇怪得**看得见** —— 而把 `/v1beta` 当成 v1 端点原样交出去，错在运行时。
 *
 * · **大小写敏感**。URL 路径本来就区分大小写，本站的路由只挂 `/v1`。
 *   `https://a.com/V1` 不是一个能用的端点，把它认成"已经带了 v1"等于把一个
 *   坏地址原样交给用户；追加成 `/V1/v1` 同样坏，但坏得当场看得见。
 *   （这条特意写下来：本仓刚抓到过一次"断言在 SQLite 上永远绿"的假测试，
 *   大小写这种判定必须先说清楚期望是什么，再落用例。）
 */

/** `scheme://`。用来把主机部分从路径部分里切出来。 */
const SCHEME_PREFIX = /^[a-z][a-z0-9+.-]*:\/\//i

/** 尾部的一个或多个斜杠。 */
const TRAILING_SLASHES = /\/+$/

/**
 * 去首尾空白与尾部斜杠。全空白（或空串）返回空串 —— 调用方据此禁用按钮，
 * 而不是让用户复制到一个空剪贴板还看到「已复制」。
 */
export function qyNormalizeApiBase(raw: string): string {
  return raw.trim().replace(TRAILING_SLASHES, '')
}

/** 取归一化后基址的路径部分（含前导斜杠）；没有路径时是空串。 */
function pathOf(base: string): string {
  const afterScheme = base.replace(SCHEME_PREFIX, '')
  const slash = afterScheme.indexOf('/')
  return slash === -1 ? '' : afterScheme.slice(slash)
}

/** 归一化基址并补上 `/v1`。已经以 `/v1` 段结尾的原样返回。 */
export function qyApiBaseWithV1(raw: string): string {
  const base = qyNormalizeApiBase(raw)
  if (base === '') return ''
  if (pathOf(base).endsWith('/v1')) return base
  return `${base}/v1`
}
