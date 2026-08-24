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
import { qyGet, qyPost } from '../../lib/api'
import type {
  QyAdminHealth,
  QyClientIPDiagnostic,
  QyLeaseList,
  QyOverdraftReport,
  QyUpdateCheck,
  QyVersionInfo,
} from './types'

export function getQyAdminHealth(): Promise<QyAdminHealth> {
  return qyGet<QyAdminHealth>('/admin/health')
}

/**
 * 版本四元组：内核版本 / 二开版本 / 同步基线 / 构建提交。
 *
 * 刻意与 `/admin/health` 分成两个请求：后端那一侧 `/admin/version` 不走
 * `requireCore`，扩展库不可用时仍然是 200，而 `/admin/health` 会 503。
 * 合并成一个请求就把这个降级能力白白丢掉了 —— 排障的第一个问题正是
 * 「现在跑的到底是哪个版本」。
 */
export function getQyVersion(): Promise<QyVersionInfo> {
  return qyGet<QyVersionInfo>('/admin/version')
}

/**
 * 检查**二开**是否有新版本。上游内核那一颗按钮是上游自己的代码，与此无关。
 *
 * 与 {@link getQyVersion} 分成两条路由是刻意的，两者性质完全不同：
 * 前者读的是编译进二进制的常量（必然成功、零副作用、管理员可读），
 * 这一条会让**服务端**向 github.com 开一次出站连接（站点行为，只有超管能点，
 * 且挂了关键操作限流）。
 *
 * 绝不自动调用：没有 `useQuery` 自动拉取，只在管理员点按钮时发一次。
 * 定时向第三方发请求是一次没人点过头的站点行为变更，而离线部署会让它永远失败。
 */
export function checkQyUpdate(): Promise<QyUpdateCheck> {
  return qyGet<QyUpdateCheck>('/admin/version/check-update')
}

/** 租约持有情况。用于确认多节点没有双跑同一个后台任务。 */
export function listQyLeases(): Promise<QyLeaseList> {
  return qyGet<QyLeaseList>('/admin/leases')
}

/**
 * 重新加载 YAML 配置。
 *
 * `database` 段永不重载 —— 连接池与 DSN 不能热切，那会让正在进行的事务
 * 落到旧连接上。其余段（费率、开关、影子模式）立即生效并写审计。
 */
export function reloadQyConfig(): Promise<{
  reloaded: boolean
  path: string
  loaded_at: number
}> {
  return qyPost('/admin/config/reload')
}

/**
 * 负余额（透支）总览。
 *
 * 刻意与 `/admin/health` 分成两个请求，理由与 `/admin/version` 那条相同：
 * 后端那一侧 `/admin/overdraft` **不走 `requireCore`**（它只查主库 users），
 * 扩展库不可用时仍然是 200，而 `/admin/health` 会 503。合并成一个请求
 * 就把这个降级能力白白丢掉了 —— 而"站上现在欠了多少钱"恰恰是排障时
 * 最不该跟着扩展库一起消失的数字。
 */
export function getQyOverdraft(): Promise<QyOverdraftReport> {
  return qyGet<QyOverdraftReport>('/admin/overdraft')
}

/**
 * 客户端 IP 识别诊断。
 *
 * 刻意与 `/admin/health` 分成两个请求，理由与 `/admin/version`、
 * `/admin/overdraft` 那两条相同：后端那一侧 **不走 `requireCore`**（它只读
 * 进程内的取值策略和本次请求自身），扩展库不可用时仍然是 200，而
 * `/admin/health` 会 503。而令牌 `allow_ips` 与按 IP 的限流根本不依赖扩展库
 * —— 它们照样在按这个值放行/拒绝，所以这份诊断绝不该跟着扩展库一起消失。
 */
export function getQyClientIP(): Promise<QyClientIPDiagnostic> {
  return qyGet<QyClientIPDiagnostic>('/admin/client-ip')
}
