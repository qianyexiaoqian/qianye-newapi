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
    /** 主库 COMMIT 已发出但结局不明。与 pending 分开报：这一档意味着钱可能已经动了。 */
    in_doubt?: number
    uncertain?: number
    /**
     * 「最老的未落定单」——扫的是 Pending + InDoubt 两档，不是只有 Pending。
     * 键名刻意不叫 oldest_pending_*：与上面那个只数 Pending 的计数同名，
     * 会让面板出现「待处理 0」配「最老待定单已挂 4 分 13 秒 + 单号 XXX」
     * 这种自相矛盾的一屏，而英文界面下两处逐字都是 pending。
     */
    oldest_unsettled_age_sec?: number
    oldest_unsettled_order_no?: string
    oldest_uncertain_age_sec?: number
    oldest_uncertain_order_no?: string
  }
  leases: QyTaskLease[]
  migrate: { table_count: number }
  /** 每个模块的配置段现状。见 `QyModuleSection`。 */
  modules: QyModuleSection[]
  config: { path: string; loaded_at: number; mtime: number }
  node: { name: string; is_master: boolean; holder: string }
  /** 分组倍率失配。见 `QyGroupRatioHealth`。 */
  group_ratio?: QyGroupRatioHealth
}

/**
 * 分组倍率失配，与 `qianye/groupratio` 的 `Health()` 对齐。
 *
 * # 它在守什么
 *
 * 上游 `ratio_setting.GetGroupRatio` 在分组名查不到时**返回 1 并只写一条
 * SysLog**。任何名字对不上（分组被改名、被从倍率表删掉、大小写写岔）都不报错、
 * 不拒绝、**静默按 1.0 倍扣费** —— 而站点上那几个 `ratio=0` 的免费分组一旦对不上，
 * 就从免费变成原价，唯一的痕迹是一行会被滚走的日志。
 *
 * 这一段是那条 fail-open 唯一常驻的信号。
 */
export type QyGroupRatioHealth = {
  /**
   * **被动**信号：扩展自己解析倍率时真的撞到过的失配名，带累计次数与首末次时间。
   * 只覆盖扩展看过的组合。
   */
  observed: QyGroupRatioMiss[]
  /** 登记簿容量上限（256）之外被丢弃的失配名数量。非零本身就是异常。 */
  observed_dropped: number
  /**
   * **主动**信号：全站 `users.group` ∪ `tokens.group` 的扫描结果。
   *
   * **字段缺失表示本进程还没扫过一次**，那不等于「没有问题」。刻意不用一个
   * 空结果去冒充，两者必须能被分开。完整报表在
   * `GET /admin/group-ratio/orphans`。
   */
  last_scan?: QyGroupRatioScan
}

export type QyGroupRatioMiss = {
  group: string
  /** 倍率表里存在一个仅大小写不同的名字。分组倍率按精确匹配，二者是两个分组。 */
  near_miss?: string
  count: number
  first_seen: number
  last_seen: number
}

export type QyGroupRatioScan = {
  at: number
  orphans: QyGroupRatioOrphan[]
  /**
   * 失配用户分组下的用户数合计。
   *
   * **大于 0 就意味着有人正在被按 1.0 倍静默计费**，这是这一段里唯一
   * 需要立刻处理的数字。
   */
  orphan_users: number
  defined_groups: number
  /** 非空 = 扫描没跑完，`orphans` 不完整。 */
  error?: string
}

export type QyGroupRatioOrphan = {
  group: string
  /** users.group 命中该名字的在册用户数。**这一栏才是资损。** */
  users: number
  /**
   * tokens.group 命中该名字的令牌数。
   *
   * **这一栏不是资损**：上游 `middleware/auth.go` 已经用 `ContainsGroupRatio`
   * 挡住了，表现是 403「分组已被弃用」，请求根本进不到计价。
   */
  tokens: number
  tokens_enabled: number
  near_miss?: string
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
 * 四个值都是编译期常量（ldflags 注入或 go:embed 编入），进程不重启就不会变。
 * 未注入/未声明时后端返回 `"unknown"`（而不是伪造一个版本号），前端原样展示 ——
 * 排障时被假版本号误导，比看不到版本号更糟。
 *
 * **`core` 与 `fork` 是两个互不相干的版本号**，不要在界面上把它们拼起来：
 * 一度有一版把二者合成 `v1.0.0-rc.25+qy.2` 塞进 `common.Version`，结果是
 * 「当前版本」那一栏既不是上游版本也不是我们的版本，而且让上游那颗检查更新
 * 按钮（它拿这个值跟 release 的 `tag_name` 做**相等比较**）永远报「有新版本」。
 */
export type QyVersionInfo = {
  /**
   * 二开当前构建的**提交**，`git describe --tags` 原样输出。构建期 ldflags 注入。
   *
   * 它不是版本号：这个值是从上游的 tag 算出来的，上游一打新 tag 它就整体跳变。
   * 它回答的是「你这台机器跑的到底是哪个提交」。版本号看 {@link fork}。
   */
  build: string
  /**
   * 同步基线的**精确提交**，取上游自己 `git describe --tags` 的输出，
   * 例 `v1.0.0-rc.25-1-g2d8e50bf3`。
   *
   * 声明在 `qianye/version/baseline.txt`，由 go:embed 编进二进制，不走 ldflags：
   * 本 fork 靠逐提交挑拣同步，挑拣不产生祖先关系，`git describe` 量的 tag 可达性
   * 会沉默地落后一个 release。
   *
   * 与 {@link core} 的差别是「精确到提交」与「与上游逐字一致」之差。
   */
  upstream: string
  /**
   * **内核版本**：上游 new-api 的版本号，逐字一致、不带任何后缀，
   * 例 `v1.0.0-rc.25`。即运行期实际的 `common.Version`。
   *
   * 未经构建脚本注入时是上游默认值 `v0.0.0` —— 那正是「这个包没按流程出」的信号。
   */
  core: string
  /**
   * **二开版本**：我们自己的版本号，恒为 `vMAJOR.MINOR.PATCH`，例 `v0.1.0`。
   *
   * 与 {@link core} 互不相干：同步一次上游不会让它进位，发一版二开也不会改内核
   * 版本。检查更新（{@link QyUpdateCheck}）比的就是它。
   */
  fork: string
}

/**
 * 二开检查更新的结论，与 `qianye/controller/update_check.go` 的
 * `AdminCheckUpdate` 响应对齐。
 *
 * **成功也有五种结局**，因为「没查出新版本」的原因不止一个，而它们在界面上
 * 必须说得不一样 —— 尤其是 `no_release`（仓库在、我们还没发过版）不能被说成
 * 「已是最新」，也不能被说成「检查失败」。
 */
export type QyUpdateCheckStatus =
  /** 远端版本比本机新。唯一需要人动手的一档。 */
  | 'update_available'
  /** 远端与本机相同。 */
  | 'up_to_date'
  /** 本机比远端新（改完还没发版）。不是错误，但也不是「已是最新」。 */
  | 'ahead'
  /** 仓库在，但一个 release 都没发过。该做的是去发版，不是去查网络。 */
  | 'no_release'
  /** 本机的二开版本号没声明/解析不了，有远端版本也判不出新旧。 */
  | 'current_unknown'
  /** 远端 tag 不在我们的版本方案里（例如手滑打了个日期式 tag）。 */
  | 'latest_unparsable'

export type QyUpdateCheck = {
  status: QyUpdateCheckStatus
  /** 本机的二开版本号，等于 {@link QyVersionInfo.fork}。 */
  current: string
  /** 远端最新 release 的 tag 原样（含 `qy-` 前缀）。`no_release` 时是空串。 */
  latest: string
  /** 远端 release 的标题。可能是空串。 */
  release_name?: string
  /**
   * 给人点的 release 页面。**绝不是下载直链** —— 本站不自动下载、不自动更新，
   * 这个链接是唯一的去处。
   */
  release_url: string
  /** ISO8601，远端 release 的发布时间。`no_release` 时是空串。 */
  published_at: string
  /** 远端 release 被标记为预发布。 */
  prerelease: boolean
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

/** 一个负余额（透支）账号。`quota` 恒为负 —— 后端刻意下发原值，见 overdraft 包注释。 */
export interface QyOverdraftAccount {
  user_id: number
  username: string
  display_name: string
  group: string
  status: number
  quota: number
}

/**
 * 负余额总览（`GET /api/qy/admin/overdraft`）。
 *
 * 存在理由不是"又一个报表"：本站**刻意接受**预扣/结算无下限（拍板与代价见
 * `qianye/docs/decisions.md` D-01），而接受透支的前提是透支看得见。
 * 这份数据是运营决定"要不要追欠费 / 封号"的依据。
 */
export interface QyOverdraftReport {
  at: number
  /** 负余额账号数。 */
  accounts: number
  /** 合计欠额，恒 >= 0（= -SUM(quota)）。 */
  total_owed: number
  /** 欠得最深的账号；没有负余额账号时为 null。 */
  deepest: QyOverdraftAccount | null
  top: QyOverdraftAccount[]
  /** true 表示 `accounts > top.length`，清单被截断了。 */
  truncated: boolean
}

/**
 * 客户端 IP 识别诊断（`GET /api/qy/admin/client-ip`），与
 * `qianye/controller/client_ip.go` 的 `AdminClientIP` 响应对齐。
 *
 * 这一段回答的不是「我的 IP 是什么」——那个值台账里就有——而是
 * **「为什么是这个值」**。客户端 IP 是四类判据的取值来源（令牌 allow_ips、
 * 按 IP 的限流、审计台账、风控去重），而它配错的两种方向**都不会报错**：
 * 配窄了全站 IP 变成反代地址，配宽了任何能打到端口的东西都能伪造来源 IP。
 */
export type QyClientIPDiagnostic = {
  /** 打开这一页的这条请求自身的完整取值过程。管理员就是一个真实样本。 */
  request: QyClientIPResolution
  policy: QyClientIPPolicy
  /** 内置 Cloudflare 网段快照的出处与取得日期。 */
  cloudflare_source: string
  /**
   * 「直连对端不受信、却带着转发头」的观测台。
   *
   * **非空基本等同于确诊**：站点装在反代/CDN 后面，但 `TRUSTED_PROXIES`
   * 没配到那个对端上。每一条都带着可以直接粘进 `TRUSTED_PROXIES` 的
   * `suggestion`。
   */
  observations: QyClientIPObservation[]
  /** 超过观测台容量上限（32 个对端）之后被丢弃的条数。 */
  observations_dropped: number
}

export type QyClientIPResolution = {
  /** 最终结论。令牌 allow_ips、限流桶、台账用的都是它。 */
  ip: string
  /** TCP 直连对端，已归一化。唯一不可伪造的事实。 */
  peer: string
  /** 对端是否落在某一档受信网段里。false 时转发头全部作废。 */
  peer_trusted: boolean
  /** 命中的受信来源名：explicit / private / loopback / cloudflare。 */
  trust_source?: string
  /** 结论是从哪个请求头取的。缺失表示结论就是 `peer`。 */
  header?: string
  /** 转发链原文（未归一化），从左到右。左端是客户端自己写的、谁都能编的前缀。 */
  chain?: string[]
  /** 因为对端不受信而被丢弃的转发头。 */
  ignored_headers?: { name: string; value: string }[]
  /**
   * 排在结论之后、但同样有值且给出**不同答案**的受信请求头。
   *
   * 它是「只配了 X-Real-IP 的 Nginx」这种错配唯一的确诊信号:那种 nginx 会把
   * 客户端自带的 X-Forwarded-For 原样透传,而默认头顺序里 XFF 排在
   * X-Real-IP 前面 —— 于是客户端能顶掉反代诚实写下的值。
   * 正确配置(显式声明 CLIENT_IP_HEADERS=X-Real-IP)下这一栏是空的。
   */
  conflicts?: { header: string; ip: string }[]
  reason: QyClientIPReason
  strategy: QyClientIPStrategy
}

export type QyClientIPReason =
  /** 直连对端不是受信代理，转发头全部忽略。 */
  | 'direct_peer'
  /** RemoteAddr 解析不出 IP。 */
  | 'peer_unparsable'
  /** 从转发链上从右往左剥出来的地址。正常反代部署就是这一档。 */
  | 'forwarded_chain'
  /** 链上每一跳都落在受信网段里，取了最左端。受信网段配得过宽时会出现。 */
  | 'forwarded_chain_all_trusted'
  /** 从单值头（CF-Connecting-IP 等）取到。 */
  | 'forwarded_header'
  /** 对端受信，但一个可用的转发头都没带 —— 反代漏了 proxy_set_header。 */
  | 'trusted_peer_no_header'

export type QyClientIPStrategy =
  /** 运维显式配了 TRUSTED_PROXIES。 */
  | 'explicit'
  /** 显式配了 TRUSTED_PROXIES=none。 */
  | 'none'
  /** 未配置：用上游那份默认（回环 + RFC1918 + fc00::/7），并打一条 WARNING。 */
  | 'default_private'

export type QyClientIPPolicy = {
  strategy: QyClientIPStrategy
  /** 原始 TRUSTED_PROXIES 取值，原样回显。 */
  raw: string
  notice: string
  /** 非空表示当前策略有已知代价（例如信任面覆盖了全部地址）。 */
  warning: string
  sources: { name: string; headers: string[]; cidrs: string[] }[]
}

export type QyClientIPObservation = {
  peer: string
  headers: string[]
  count: number
  first_seen: number
  last_seen: number
  /** 可以直接写进 TRUSTED_PROXIES 的值（/32 或 /128 单机地址）。 */
  suggestion: string
}
