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
 * 模型按分组单独定价的管理端 DTO。
 *
 * 字段与 `qianye/modules/grouppricing/` 逐个对齐：
 *   - 规则本体 `model.go` 的 `Rule`
 *   - 折算结果 `effective.go` 的 `Effective`
 *   - 对账     `reconcile.go` 的 `ShadowSummary` / `ShadowSegment`
 *
 * **所有价格、倍率、系数都是十进制字符串。** 后端刻意用 `decimal` 存储并以字符串
 * 传输，前端就不能在中途把它变回 number —— `0.1` 经过一次 float64 往返会变成
 * `0.10000000000000001`，而这个数字会被乘进用户的账单。前端对它们只做展示、
 * 比较大小、原样回传。
 */

/**
 * 覆盖口径，三选一，互斥。
 *
 *   - `price`  按次固定价（`ratio_setting.GetModelPrice`），单位美元/次；
 *   - `ratio`  按 token 的模型倍率（`ratio_setting.GetModelRatio`）；
 *   - `tiered` 阶梯表达式计价的**乘数**（阶梯价是一整条表达式，没法用标量替换）。
 *
 * 三者都与分组倍率相乘，因此折算公式对三种口径通用。
 */
export type QyGpMode = 'price' | 'ratio' | 'tiered'

/**
 * 一条规则在某个分组下折算出来的最终生效价。**由后端算，前端只负责显示。**
 *
 * 前端刻意不自己再乘一遍：两套实现迟早会有一处漏乘分组倍率或多舍一位，
 * 而管理端显示的数字与实际扣费不一致，比不显示更糟。
 */
export type QyGpEffective = {
  /** 该分组当前的分组倍率。 */
  group_ratio: string
  /** 该模型当前的全局价 / 倍率；未配置或通配规则时缺省。 */
  global_value?: string
  /** 全局价 × 分组倍率 = **改动前**这个分组实际付的价。 */
  global_effective?: string
  /** 规则里填的分组级价 / 倍率。 */
  rule_value: string
  /** 分组级价 × 分组倍率 = **改动后**这个分组实际付的价。 */
  rule_effective: string
  /** 相对改动前的涨跌幅，形如 `-25.00`（已是百分数，不带 `%`）。无基准时缺省。 */
  delta_percent?: string
  /** `rule_effective` 的单位文案，后端下发（中文）。 */
  unit: string
  /** 仅 `price` 口径：一次调用实际扣多少 quota。 */
  quota_per_call?: number
  /**
   * 「这条规则配了也不会生效」的显式告警（中文原文）。
   *
   * 必须持续显示在列表里：一条静默不生效的价格规则与一个定义了却没有消费方的
   * 配置项是同一种缺陷，而这个扩展已经在那上面栽过四次。
   */
  warning?: string
}

/**
 * 一条 (分组, 模型) → 价格覆盖 规则。
 *
 * `model_name` 支持三种形态，优先级 精确 > 最长前缀 > `*`：
 * `gpt-4o` / `gpt-4*` / `*`。因此模型输入框不能锁死成纯下拉。
 */
export type QyGpRule = {
  id: number
  group_name: string
  model_name: string
  enabled: boolean
  mode: QyGpMode
  /** 覆盖值，按 `mode` 解释。十进制字符串，最多 10 位小数。 */
  value: string
  remark: string
  created_at: number
  updated_at: number
  created_by: number
  updated_by: number
  effective: QyGpEffective
}

export type QyGpRulesPage = {
  items: QyGpRule[]
  total: number
  /**
   * true = 影子模式：完整算出「若启用会扣多少」并记录差额，实际扣费一分不变。
   *
   * 跟着列表一起返回是刻意的：同一张列表在影子模式下是预演、在真实模式下
   * 是正在扣的钱，两者长得一模一样。
   */
  shadow_mode: boolean
}

/** 新建 / 编辑 / 试算 共用的入参。 */
export type QyGpRuleInput = {
  group_name: string
  model_name: string
  mode: QyGpMode
  /** 十进制字符串，原样传给后端，前端不做 `Number()` 往返。 */
  value: string
  enabled: boolean
  remark: string
}

// ───────────────────────────── 影子差额对账 ─────────────────────────────

/**
 * 一段「规则值保持不变」的区间在某个 (分组, 模型) 上的汇总。
 *
 * 差额不是估算：扣费对被覆盖的那个因子是严格线性的，所以
 * `差额 = 分摊到本段的实际扣费 × (系数 - 1)` 是精确值。
 */
export type QyGpShadowSegment = {
  group_name: string
  model_name: string
  mode: QyGpMode
  old_value: string
  new_value: string
  /**
   * false = 这一段无法按比例折算（旧值为 0，或计价口径发生了切换）。
   * 这类行的 `delta_quota` 恒为 0 并被单独计数，**绝不能混进合计里假装精确**。
   */
  exact: boolean
  inexact_reason?: string
  requests: number
  /** 最近一次命中的请求标识，供运营抽一条去核对。 */
  sample_request_id?: string
  /** 该 (分组, 模型) 在整个区间内的真实扣费合计（维度比本段粗）。 */
  actual_quota: number
  /** 本段请求数占该 (分组, 模型) 的比例，形如 `0.400000`。 */
  request_share: string
  /** false = 该 (分组, 模型) 区间内换过规则值，金额是按请求数占比分摊的。 */
  share_is_exact: boolean
  /** 分摊到本段的实际扣费。 */
  attributed_quota: number
  /** 新值 / 旧值。 */
  factor: string
  /** 正 = 切换后多收，负 = 少收。 */
  delta_quota: number
}

export type QyGpShadowSummary = {
  start: number
  end: number
  segments: QyGpShadowSegment[]
  total_requests: number
  /** 可折算部分当前的真实扣费合计。 */
  total_actual_quota: number
  /** 切换后的净变化：正 = 多收，负 = 少收。 */
  total_delta_quota: number
  /** 无法折算的请求数。必须单独露出来，否则「合计」看起来完整而实际漏了一块。 */
  inexact_requests: number
  /** 维度组合数超上限，只取了请求数最多的前若干组。 */
  truncated: boolean
  /**
   * 非空 = 主库日志聚合失败，金额列全为 0，只有请求数与系数可信。
   * 页面必须当场说明，否则运营会拿一份缺金额的报表去做上线决策。
   */
  quota_source_error?: string
}

export type QyGpShadowResponse = {
  summary: QyGpShadowSummary
  shadow_mode: boolean
}

/** 对账图表的聚合维度。纯前端状态，后端只给到段粒度。 */
export type QyGpShadowDimension = 'group' | 'model'
