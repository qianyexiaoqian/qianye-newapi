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
/** 与 `qianye/modules/violation/model.go` 的 Record / Ban / Appeal 对齐。 */

export type QyViolationRecordStatus = 'active' | 'appealed' | 'revoked'

/** 扣费结果。`truncated` / `insufficient` 是余额不足留下的可审计偏差。 */
export type QyViolationFeeStatus =
  | 'charged'
  | 'failed'
  | 'insufficient'
  | 'none'
  | 'refunded'
  | 'shadow'
  | 'skipped_dup_builtin'
  | 'truncated'
  | (string & {})

export type QyViolationRecord = {
  id: number
  rec_no: string
  user_id: number
  username: string
  token_id: number
  token_name: string
  rule_id: number
  rule_name: string
  /** 冻结命中当时的对外文案，规则改名后用户端仍应显示原文。 */
  public_reason: string
  phase: string
  action: string
  /** 影子模式命中：只记不罚，用户并未被扣钱。 */
  shadow: boolean
  blocked: boolean
  model_name: string
  using_group: string
  channel_id: number
  relay_format: string
  /** 与主库 `logs.request_id` 对齐，是与计费日志对账的唯一钥匙。 */
  request_id: string
  ip: string
  matched_terms: string
  match_snippet: string
  fee_mode: string
  fee_base_usd: string
  fee_multiple: string
  group_ratio: string
  fee_quota_want: number
  fee_quota: number
  fee_status: QyViolationFeeStatus
  fee_error: string
  /** 额度饱和的审计标记 JSON，非空几乎必然意味着规则配错。 */
  quota_clamp: string
  count_weight: number
  counted: boolean
  counter_after: number
  status: QyViolationRecordStatus
  revoked_by: number
  revoked_at: number
  revoke_reason: string
  refund_quota: number
  has_payload: boolean
  created_at: number
}

/** `GET /admin/violation/records/:id/evidence` 的响应。 */
export type QyViolationEvidence = {
  record: QyViolationRecord
  has_payload: boolean
  context?: string
  /** 多模态描述符 JSON 数组（哈希 / 大小 / MIME），绝不含二进制。 */
  files?: string
  truncated?: boolean
  redacted?: boolean
  redact_stats?: string
  origin_bytes?: number
  stored_bytes?: number
}

export type QyViolationBanStatus =
  | 'banned'
  | 'failed'
  /**
   * 计数达到了阈值，但当时生效的策略档只要求「仅记录」。
   *
   * 它是终态：补偿任务不会执行它，解封接口也不接受它（从来没有人被这一行禁用过）。
   * 它存在的唯一理由是让管理员看得见「如果把这一档切成封号，现在会封掉谁」——
   * 那份名单只能来自封禁列表，只打日志的话既不可筛选也不可分页。
   */
  | 'observed'
  | 'pending'
  | 'skipped'
  | 'unbanned'
  | (string & {})

export type QyViolationBan = {
  id: number
  user_id: number
  /** 每次解封 +1。不 +1 会让该用户的自动封号从此静默失效。 */
  ban_cycle: number
  trigger_record_id: number
  hit_count_at: number
  threshold: number
  /**
   * 冻结「当时是哪一档策略、判了什么动作」。
   *
   * 阈值改成按分组可配之后，「这个人为什么在第 5 次就被处置」不再能从任何全局
   * 配置反推 —— 用户的分组会变，策略档会被编辑。`policy_group` 为空串表示
   * 当时落的是兜底档。
   */
  policy_group: string
  policy_action: '' | QyViolationBanPolicyAction
  status: QyViolationBanStatus
  attempts: number
  last_error: string
  banned_at: number
  unbanned_at: number
  unbanned_by: number
  unban_note: string
  created_at: number
}

export type QyViolationAppealStatus =
  | 'approved'
  | 'pending'
  | 'rejected'
  | 'withdrawn'
  | (string & {})

export type QyViolationAppeal = {
  id: number
  user_id: number
  record_id: number
  reason: string
  status: QyViolationAppealStatus
  reviewer_id: number
  review_note: string
  reviewed_at: number
  created_at: number
  updated_at: number
}

/* ─────────────────────── 按用户分组的处置策略档 ─────────────────────── */

/**
 * 达到阈值之后的处置动作。
 *
 * **主库只有一个非删除停用态**（`common.UserStatusDisabled`，语义是「受限账号：
 * 能登录、只能提工单，其余一律 403」），所以 `restrict` 与 `ban` 落到
 * `users.status` 上是同一个值。差别只有一处：`ban` 会额外吊销全部登录会话，
 * 把用户强制登出。两档在 `auth_version` 上一样（都递增，残留 JWT 一律失效），
 * 因此 `restrict` **不是**一个更弱的安全边界，它只是不主动把人踢出控制台。
 *
 * 界面上必须把这句话说清楚，否则「限制」和「封号」看起来像两种账号状态。
 */
export type QyViolationBanPolicyAction = 'ban' | 'record' | 'restrict'

/** 与 `qianye/modules/violation/model.go` 的 BanPolicy 对齐。 */
export type QyViolationBanPolicy = {
  id: number
  /** 归一化后的用户分组名。兜底档恒为空串。 */
  user_group: string
  /** 兜底档：没有专属档的用户分组都按它判。**永远存在且不可删。** */
  is_default: boolean
  /** 停用一档 = 该分组回落兜底档，**不是**「这个分组从此免罚」。 */
  enabled: boolean
  window_hours: number
  /** 0 表示这一档不做任何自动处置。 */
  threshold: number
  action: QyViolationBanPolicyAction
  remark: string
  created_at: number
  updated_at: number
  updated_by: number
}

/**
 * 「改成这套阈值，立刻会处置几个人」。
 *
 * 改阈值不是改一个配置项，是**立刻处置一批已经越线的存量账号**：计数是滚动窗口里
 * 早就攒好的。没有这个数，管理员按下保存时手上唯一的信息是「我把 10 改成了 3」。
 */
export type QyViolationBanPolicyImpact = {
  matched: number
  /** 扫描到了上限，真实值 >= `matched`。 */
  capped: boolean
  scanned: number
  action: string
  threshold: number
  window_hours: number
  /** 前若干个样本账号，给管理员一个「到底是谁」的抓手。 */
  user_ids: number[]
}

export type QyViolationBanPolicyList = {
  items: QyViolationBanPolicy[]
  /**
   * 兜底值是不是真的来自库里那一行。
   *
   * 为 `false` 时兜底跑在 YAML 上（库里没有兜底行，或快照从未加载成功），
   * 此时在这张表里改任何东西都不会影响没配分组的用户 —— 不显示这一位的话，
   * 这个落差只有读源码才能发现。
   */
  fallback_from_db: boolean
  fallback: {
    window_hours: number
    threshold: number
    action: QyViolationBanPolicyAction
  }
  actions: { value: QyViolationBanPolicyAction; label: string }[]
}

export type QyViolationBanPolicyInput = {
  user_group: string
  enabled: boolean
  window_hours: number
  threshold: number
  action: QyViolationBanPolicyAction
  remark: string
  /** 会扩大处置面的写入必须带它，否则后端回 409。 */
  confirm: boolean
}
