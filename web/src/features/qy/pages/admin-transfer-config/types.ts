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
 * 划转门槛管理端 DTO。对应 `qianye/modules/transfer/api_admin_config.go`。
 *
 * 键名与后端 `settings.go` 的常量逐字一致 —— 它们同时是 `qy_settings` 表里的
 * 行键与 PUT 请求体的字段名，改一个字就写不进去。
 *
 * 取值全部是**整数**（划转没有百分比字段，`fee_bps` 本身就是万分比整数），
 * 因此这里可以安全地用 `number`：最大的 `min_quota` 上界是全站额度上界
 * `common.MaxQuota`（2^43），而那个数正是**按 `Number.MAX_SAFE_INTEGER`
 * (2^53-1) 选的**——额度以 JSON 数字下发，越过它前端读到的就不是后端写下的数。
 */
export type QyTransferEffective = {
  min_quota: number
  max_per_tx_quota: number
  daily_max_quota: number
  daily_max_count: number
  cooldown_seconds: number
  receiver_daily_max_in_count: number
  new_account_freeze_hours: number
  fee_bps: number
  fee_min_quota: number
  /** 支付密码连续输错多少次后锁定。下界是 1：0 等于「永不锁定」。 */
  pay_pwd_max_attempts: number
  /** 锁定时长（分钟）。下界同样是 1。 */
  pay_pwd_lock_minutes: number
}

/**
 * 一个键的取值闭区间，由后端下发。
 *
 * **前端不再自己抄一份区间**：后端 `settingBounds` 是唯一定义，读取路径
 * （防手工改库）、写入校验、这里的输入框校验共用它。两份区间迟早会漂移成
 * 「界面允许、后端 400」或者更糟的「界面拒绝、后端放行」。
 */
export type QyTransferBound = { min: number; max: number }

/**
 * YAML 只读段。
 *
 * 这四项是安全口径与结构性配置，只能改文件后重载（裁决 5）：
 * `enabled` 是功能总闸；`recipient_lookup` 决定是否开放按邮箱查收款人，
 * 那是一道用户枚举面；`require_receiver_enabled` 决定封禁账号能否收款；
 * `lookup_log_retain_days` 是含用户输入的日志保留期。
 */
export type QyTransferYamlReadonly = {
  enabled: boolean
  recipient_lookup: string
  require_receiver_enabled: boolean
  lookup_log_retain_days: number
}

export type QyTransferAdminConfig = {
  /** YAML 与运营覆盖合并后的**生效值**，也就是用户提交划转时真正会被判定的那份。 */
  effective: QyTransferEffective
  /**
   * 当前这组生效值是否自洽（例如单笔下限不得高于单笔上限）。
   *
   * `false` 只可能来自有人绕过接口直接改了 `qy_settings`。此时后端已经把
   * 划转停掉了（资金路径失败关闭），但这一页仍然可达 —— 它是修复那一行的
   * 唯一界面。界面必须把这件事说出来，否则运营只会看到「划转怎么用不了了」。
   */
  effective_valid: boolean
  /** `qy_settings` 里的运营覆盖，值一律是字符串。没有覆盖的键不出现。 */
  overrides: Record<string, string>
  editable_keys: string[]
  bounds: Record<string, QyTransferBound>
  yaml_readonly: QyTransferYamlReadonly
  /** YAML 里那份门槛的原值：清掉覆盖之后会回到这里。 */
  yaml_defaults: QyTransferEffective
}
