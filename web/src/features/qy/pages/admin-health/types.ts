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
import type { QyHotQueueStats } from '../ops/types'

/** 与 `qianye/controller/admin.go` 的 `AdminHealth` 响应对齐。 */
export type QyAdminHealth = {
  /** `db.Stats()`。连接池字段在句柄未建立时整组缺失。 */
  db: {
    available: boolean
    connected: boolean
    /** 熔断打开到该时刻为止（unix 秒），0 表示未熔断。 */
    breaker_open_until: number
    fail_streak: number
    last_ping_ms: number
    last_ping_at: number
    open_conns?: number
    in_use?: number
    idle?: number
    wait_count?: number
    max_open?: number
  }
  hot_queue: QyHotQueueStats
  /** `twophase.Stats()`。扩展库不可用时为空对象。 */
  two_phase: {
    pending?: number
    uncertain?: number
    oldest_pending_age_sec?: number
    oldest_pending_order_no?: string
  }
  leases: QyTaskLease[]
  migrate: { table_count: number }
  /** 每个模块的配置段现状。见 `QyModuleSection`。 */
  modules: QyModuleSection[]
  config: { path: string; loaded_at: number; mtime: number }
  node: { name: string; is_master: boolean; holder: string }
}

/**
 * 单个模块的配置段现状，与 `qianye/config/sections.go` 的 `ModuleSection` 对齐。
 *
 * 存在的理由：模块级的 `enabled` 在后端是普通布尔，零值 `false` ——
 * 「配置文件里根本没有这一段」与「运维想清楚了、显式写了 `enabled: false`」
 * 在进程内是同一个字节。这个歧义造成过三次生产事故，表现都是
 * 「代码全都编译进去了，刷新却看不到功能」。
 *
 * 因此 `state` 与 `enabled` 要一起看：`enabled=false` 且 `state='declared'`
 * 是正常的；`enabled=false` 且 `state='missing_section'` 八成不是任何人想要的。
 */
export type QyModuleSection = {
  /**
   * 模块名，与后端注册表的 `Name()` 一致。
   *
   * **不唯一**：一个模块的总开关与它段内的二级开关各占一行（`violation`
   * 有 `enabled` / `precheck_enabled` / `post_charge_enabled` 三行）。
   * 行 key 必须是 `module + key`。
   */
  module: string
  /** 顶层 yaml 段名。空串表示该模块没有配置段。 */
  section: string
  /** 段内的开关键名。空串表示该段没有总开关。 */
  key: string
  state: QyModuleSectionState
  /**
   * 这个开关此刻的实际取值。
   *
   * `state='ungated'`（该模块没有开关）时它是**扩展总开关**的取值 ——
   * 那几个模块随扩展一起生效。这里曾经恒为 `false`，于是面板把 5 个正在
   * 工作的模块显示成「当前生效：否」，排障的人据此去找一个不存在的开关。
   */
  enabled: boolean
  /** 这一段缺失时会出现的现象，后端直接给出可读文案。 */
  effect: string
  /**
   * 可粘进配置文件的最小片段，仅在两种 missing 状态下存在。
   *
   * **两种状态给的东西不一样**：`missing_section` 是一整段（追加到文件末尾），
   * `missing_key` 只有段内那一行（补进已经存在的那一段里）。把后者当成新的
   * 顶层段追加会产生重复的顶层 YAML 键，配置从此解析失败、网关起不来 ——
   * 所以展示时必须连「往哪儿粘」一起说。
   */
  fix?: string
}

export type QyModuleSectionState =
  /** 开关键被显式写出来了（true / false 都算）。运维做过决定，正常。 */
  | 'declared'
  /** 顶层压根没有这一段 —— 模块静默关闭，需要处理。 */
  | 'missing_section'
  /** 段写了但没有总开关那一行 —— 同样静默关闭，而且更隐蔽。 */
  | 'missing_key'
  /** 开关是「默认打开」型，不写也不会静默失效。 */
  | 'default_on'
  /** 该模块没有配置开关，随扩展总开关一起生效。 */
  | 'ungated'

/**
 * 与 `qianye/controller/version.go` 的 `AdminVersion` 响应对齐。
 *
 * 三个值都是构建期 ldflags 注入的编译期常量，进程不重启就不会变。
 * 未注入时后端返回 `"unknown"`（而不是伪造一个版本号），前端原样展示 ——
 * 排障时被假版本号误导，比看不到版本号更糟。
 */
export type QyVersionInfo = {
  /** 二开当前版本，`git describe --tags` 原样输出。 */
  build: string
  /** 最近一次同步到的上游 tag。 */
  upstream: string
  /** 上游自己的版本号（`common.Version`），未注入时是它的默认值 `v0.0.0`。 */
  core: string
}

export type QyTaskLease = {
  name: string
  /** `NodeName:PID`。同机多实例会重名，所以不能只看 NodeName。 */
  holder: string
  /** 每次易主递增；老持有者恢复后 fence 已过期，写入会失败，不会双跑。 */
  fence: number
  lease_until: number
  acquired_at: number
  updated_at?: number
}

/** `GET /admin/leases` 比 `/admin/health` 多一个已算好的 `expired`。 */
export type QyLeaseListItem = QyTaskLease & { expired: boolean }

export type QyLeaseList = {
  items: QyLeaseListItem[]
  /** 当前节点的 holder 标识，用于在表里高亮「就是我」。 */
  self: string
}
