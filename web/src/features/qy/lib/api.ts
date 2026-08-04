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
import type { TFunction } from 'i18next'

import { api, type ApiRequestConfig } from '@/lib/api'

import type { QyEnvelope } from './types'

/**
 * qy 扩展的 HTTP 客户端。
 *
 * 刻意复用 `@/lib/http-client` 的 axios 实例而不是新建一个：那个实例已经带了
 * `withCredentials`、请求拦截器注入 Bearer、401 自动刷新并重试一次、GET 在途
 * 去重四项能力，新建实例等于把这四样重写一遍并埋四个坑。
 *
 * 本层只做两件上游实例做不了的事：
 *   1. 统一解包 `{success,message,code,data}` 信封；
 *   2. 把 HTTP 状态码 + 业务 code 归一成 {@link QyFailureKind}，让调用方能按
 *      语义分流（隐藏入口 / 提示重试 / 刷新列表），而不是对着字符串 message 做判断。
 */

export const QY_API_PREFIX = '/api/qy'

/**
 * 失败语义分类。
 *
 * 与后端 `qianye/guard/guard.go` 的降级契约对齐：
 *   - disabled / feature_off → 404，前端**静默隐藏入口**，不弹任何 toast；
 *   - unavailable            → 503，扩展库不可用，提示"稍后重试"空态；
 *   - 其余按 HTTP 语义细分，便于各页面决定要不要 invalidate 缓存。
 */
export type QyFailureKind =
  | 'business' // 200 + success:false，后端 message 是唯一信息来源
  | 'conflict' // 409，单据已被其他管理员处理
  | 'disabled' // 扩展整体未启用（含未注册路由时的 NoRoute 兜底）
  | 'feature_off' // 扩展启用但该功能开关关闭
  | 'forbidden' // 403，权限不足
  | 'invalid' // 400，请求参数不合法
  | 'network' // 网络错误 / 无响应，请求是否已在服务端生效**无法判定**
  | 'rate_limited' // 429
  | 'server' // 5xx（503 除外）
  | 'unavailable' // 503，扩展库不可用

/** 后端 guard 层的固定 code，见 `qianye/guard/guard.go`。 */
const CODE_DISABLED = 'qy_disabled'
const CODE_FEATURE_OFF = 'qy_feature_off'
const CODE_UNAVAILABLE = 'qy_unavailable'

const KIND_I18N_KEY: Record<QyFailureKind, string> = {
  business: 'qy_err_unknown',
  conflict: 'qy_err_conflict',
  disabled: 'qy_err_disabled',
  feature_off: 'qy_err_feature_off',
  forbidden: 'qy_err_forbidden',
  invalid: 'qy_err_invalid',
  network: 'qy_err_network',
  rate_limited: 'qy_err_rate_limited',
  server: 'qy_err_server',
  unavailable: 'qy_err_unavailable',
}

/**
 * 后端 code → i18n key 的白名单。
 *
 * 只登记"前端有更好说法"的 code。未登记的 code 一律回落到 kind 级文案，再回落
 * 到后端原始 message —— 这样新增后端 code 不会让前端显示空白。
 *
 * 各功能模块可以直接往这里加行，key 必须同时出现在 `src/i18n/qy/{en,zh}.json`。
 */
export const QY_ERROR_CODE_I18N: Record<string, string> = {
  qy_disabled: 'qy_err_disabled',
  qy_feature_off: 'qy_err_feature_off',
  qy_unavailable: 'qy_err_unavailable',
  qy_internal_error: 'qy_err_server',
  qy_insufficient_quota: 'qy_err_insufficient',
  qy_limit_single: 'qy_err_limit_single',
  qy_limit_daily: 'qy_err_limit_daily',
  qy_self_transfer: 'qy_err_self_transfer',
  qy_recipient_invalid: 'qy_err_recipient_invalid',
  qy_uncertain: 'qy_err_uncertain',

  // ── 划转（qianye/modules/transfer/errors.go）──
  qy_confirm_required: 'qy_err_confirm_required',
  qy_idem_key_required: 'qy_err_idem_required',
  qy_amount_out_of_range: 'qy_err_amount_range',
  qy_sender_disabled: 'qy_err_sender_disabled',
  qy_account_too_new: 'qy_err_account_too_new',
  qy_receiver_not_found: 'qy_err_receiver_not_found',
  qy_receiver_disabled: 'qy_err_receiver_disabled',
  qy_receiver_overflow: 'qy_err_receiver_overflow',
  qy_daily_limit_exceeded: 'qy_err_daily_limit',
  qy_daily_count_exceeded: 'qy_err_daily_count',
  qy_cooldown: 'qy_err_cooldown',
  qy_pending_exists: 'qy_err_pending_exists',
  qy_in_progress: 'qy_err_in_progress',
  qy_transfer_failed: 'qy_err_transfer_failed',
  // 分组限制（qianye/modules/transfer/grouprule.go）。两个 code 必须映射到
  // 两句不同的话：blocked 是「换谁都不行」，denied 是「换个收款人也许就行」。
  qy_transfer_group_blocked: 'qy_err_transfer_group_blocked',
  qy_transfer_group_denied: 'qy_err_transfer_group_denied',
  // 划转联系人（qianye/modules/transfer/contacts.go）。
  qy_contact_not_found: 'qy_err_contact_not_found',
  qy_contact_user_not_found: 'qy_err_contact_user_not_found',
  qy_contact_self: 'qy_err_contact_self',
  qy_contact_duplicate: 'qy_err_contact_duplicate',
  qy_contact_limit: 'qy_err_contact_limit',

  // ── 支付密码（qianye/modules/paypass/errors.go）──
  // 四个验密结果必须映射到四句不同的话：引导去设置 / 请输入 / 输错了 /
  // 已锁定，它们要求用户做的下一步动作完全不同，混成一句用户就不知道该干嘛。
  qy_pay_pwd_not_set: 'qy_pp_err_not_set',
  qy_pay_pwd_required: 'qy_pp_err_required',
  qy_pay_pwd_wrong: 'qy_pp_err_wrong',
  qy_pay_pwd_locked: 'qy_pp_err_locked',
  qy_pay_pwd_already_set: 'qy_pp_err_already_set',
  qy_pay_pwd_weak: 'qy_pp_err_weak',
  qy_pay_pwd_same_as_old: 'qy_pp_err_same_as_old',
  qy_pay_pwd_email_unbound: 'qy_pp_err_email_unbound',
  qy_pay_pwd_code_invalid: 'qy_pp_err_code_invalid',
  qy_pay_pwd_mail_unavailable: 'qy_pp_err_mail_unavailable',
  qy_pay_pwd_user_not_found: 'qy_pp_err_user_not_found',

  // ── 提现（qianye/modules/withdraw/errors.go）──
  qy_wd_method_not_allowed: 'qy_err_wd_method_not_allowed',
  qy_wd_amount_too_small: 'qy_err_wd_amount_too_small',
  qy_wd_amount_out_of_range: 'qy_err_wd_amount_range',
  qy_wd_remark_too_long: 'qy_err_wd_remark_too_long',
  qy_wd_payee_required: 'qy_err_wd_payee_required',
  qy_wd_payee_invalid: 'qy_err_wd_payee_invalid',
  qy_wd_payee_not_found: 'qy_err_wd_payee_not_found',
  qy_wd_payee_limit: 'qy_err_wd_payee_limit',
  qy_wd_reason_required: 'qy_err_wd_reason_required',
  qy_wd_payout_ref_required: 'qy_err_wd_payout_ref_required',
  qy_wd_user_unavailable: 'qy_err_wd_user_unavailable',
  qy_wd_insufficient_commission: 'qy_err_wd_insufficient',
  qy_wd_debt_blocked: 'qy_err_wd_debt_blocked',
  qy_wd_quota_overflow: 'qy_err_wd_quota_overflow',
  qy_wd_daily_count_reached: 'qy_err_wd_daily_count',
  qy_wd_fiat_below_min: 'qy_err_wd_fiat_below_min',
  qy_wd_fee_eats_all: 'qy_err_wd_fee_eats_all',
  qy_wd_not_found: 'qy_err_wd_not_found',
  qy_wd_status_conflict: 'qy_err_wd_status_conflict',
  qy_wd_illegal_transition: 'qy_err_wd_illegal_transition',
  qy_wd_in_progress: 'qy_err_in_progress',
  qy_wd_pii_key_unavailable: 'qy_err_wd_pii_unavailable',
  qy_wd_rate_unavailable: 'qy_err_wd_rate_unavailable',
  qy_wd_payee_undecryptable: 'qy_err_wd_payee_undecryptable',
  // 凭证图片（qianye/modules/withdraw/proof.go）。后端把它拆成八个 code 正是因为
  // 用户能做的下一步完全不同：换一张图 / 压缩一下 / 先把已传的用掉 / 找管理员，
  // 前端合并成一句"上传失败"会让人反复重试同一张必然失败的图。
  qy_wd_proof_disabled: 'qy_err_wd_proof_disabled',
  qy_wd_proof_required: 'qy_err_wd_proof_required',
  qy_wd_proof_too_large: 'qy_err_wd_proof_too_large',
  qy_wd_proof_type: 'qy_err_wd_proof_type',
  qy_wd_proof_not_found: 'qy_err_wd_proof_not_found',
  qy_wd_proof_pending_limit: 'qy_err_wd_proof_pending_limit',
  qy_wd_proof_purged: 'qy_err_wd_proof_purged',
  qy_wd_proof_store_failed: 'qy_err_wd_proof_store_failed',

  // ── 工单（qianye/modules/ticket/errors.go）──
  // 四道防滥用闸门必须映射到四句不同的话：它们要求用户做的下一步完全不同 ——
  // 等一会儿 / 明天再来 / 先把手上的单关掉 / 这张单聊太长了另开一张。
  // 合并成一句"操作过于频繁"只会让人反复重试同一个必然失败的请求。
  qy_tk_title_required: 'qy_err_tk_title_required',
  qy_tk_title_too_long: 'qy_err_tk_title_too_long',
  qy_tk_body_required: 'qy_err_tk_body_required',
  qy_tk_body_too_long: 'qy_err_tk_body_too_long',
  qy_tk_priority_invalid: 'qy_err_tk_priority_invalid',
  qy_tk_assignee_invalid: 'qy_err_tk_assignee_invalid',
  qy_tk_cooldown: 'qy_err_tk_cooldown',
  qy_tk_reply_cooldown: 'qy_err_tk_reply_cooldown',
  qy_tk_daily_limit: 'qy_err_tk_daily_limit',
  qy_tk_open_limit: 'qy_err_tk_open_limit',
  qy_tk_message_limit: 'qy_err_tk_message_limit',
  qy_tk_not_found: 'qy_err_tk_not_found',
  qy_tk_closed: 'qy_err_tk_closed',
  qy_tk_illegal_transition: 'qy_err_tk_illegal_transition',
  qy_tk_status_conflict: 'qy_err_tk_status_conflict',
  // 图片八个 code 同提现凭证：合并成"上传失败"会让人反复重试同一张图。
  qy_tk_image_disabled: 'qy_err_tk_image_disabled',
  qy_tk_image_required: 'qy_err_tk_image_required',
  qy_tk_image_too_large: 'qy_err_tk_image_too_large',
  qy_tk_image_type: 'qy_err_tk_image_type',
  qy_tk_image_not_found: 'qy_err_tk_image_not_found',
  qy_tk_image_too_many: 'qy_err_tk_image_too_many',
  qy_tk_image_pending_limit: 'qy_err_tk_image_pending_limit',
  // 配额与 pending 上限必须是两句话：前者的下一步是"关掉旧工单让保留期把图片
  // 收走"，后者是"把手上这条消息发出去"。说错方向 = 让用户去做一个不生效的动作。
  qy_tk_image_quota: 'qy_err_tk_image_quota',
  qy_tk_image_purged: 'qy_err_tk_image_purged',
  qy_tk_image_store_failed: 'qy_err_tk_image_store_failed',

  // ── API 地址簿（qianye/modules/apiaddr/errors.go）──
  //
  // 不登记的话这 13 个 code 会按 HTTP 状态码归类：409 → `qy_err_conflict`
  //（"该申请已被其他人处理"，与地址簿毫不相干）、400 → "请求参数不合法"。
  // 管理员既不知道是重复、还是少了 scheme，也不知道该改哪里。
  qy_apiaddr_name_required: 'qy_err_aa_name_required',
  qy_apiaddr_name_too_long: 'qy_err_aa_name_too_long',
  qy_apiaddr_remark_too_long: 'qy_err_aa_remark_too_long',
  qy_apiaddr_url_required: 'qy_err_aa_url_required',
  qy_apiaddr_url_too_long: 'qy_err_aa_url_too_long',
  qy_apiaddr_url_scheme: 'qy_err_aa_url_scheme',
  qy_apiaddr_url_credentials: 'qy_err_aa_url_credentials',
  qy_apiaddr_url_extra: 'qy_err_aa_url_extra',
  qy_apiaddr_url_invalid: 'qy_err_aa_url_invalid',
  qy_apiaddr_duplicate: 'qy_err_aa_duplicate',
  qy_apiaddr_limit: 'qy_err_aa_limit',
  qy_apiaddr_not_found: 'qy_err_aa_not_found',
  // 并发重排被拒的下一步是"刷新后重新排"，塌成"该申请已被其他人处理"
  // 会让管理员以为自己什么都不用做。
  qy_apiaddr_order_stale: 'qy_err_aa_order_stale',

  // ── 返佣管理端（qianye/modules/commission/api_admin.go）──
  qy_reason_required: 'qy_err_cm_reason_required',
  qy_clawback_failed: 'qy_err_cm_clawback_failed',
  // 已提现额度迁移编辑（qianye/modules/commission/api_admin_balance.go）。
  // 三个 code 必须映射到三句不同的话：over_available 是「这个数填大了」，
  // over_earned 是「这行账本本来就是坏的」，overflow 是「回退幅度越界」。
  qy_withdrawn_over_available: 'qy_err_cb_over_available',
  qy_withdrawn_over_earned: 'qy_err_cb_over_earned',
  qy_withdrawn_overflow: 'qy_err_cb_overflow',

  // ── 订阅套餐（qianye/modules/subscription）──
  // 不登记的话这四个 code 会被按 HTTP 状态码归类：409 → `qy_err_conflict`
  //（"该申请已被其他人处理"，与套餐毫不相干）、400 → "请求参数有误"。
  // 管理员拿不到任何可执行的下一步，只能去猜。
  qy_subscription_plan_in_use: 'qy_err_plan_in_use',
  qy_subscription_delete_reason_required: 'qy_err_plan_delete_reason_required',
  qy_subscription_seat_invalid: 'qy_err_plan_seat_invalid',
  qy_subscription_plan_not_found: 'qy_err_plan_not_found',

  // ── 抽奖 / 竞猜（qianye/modules/lottery）──
  //
  // code 一律取自 `qianye/modules/lottery/errors.go` 的常量，**一个字都不能猜**：
  // 没登记的 code 会回落成按 HTTP 状态码归类的泛化文案，而这里恰恰是同一个 409
  // 底下藏着七种含义完全不同的失败（名额满 / 冷却中 / 同 IP 已有人 / 上一笔还没
  // 落定 / 余额不足 / 已错过封盘 / 状态变了）。最要命的是 entry_excluded ——
  // 钱确实扣了、会自动退回，说成一句"操作冲突"用户会以为钱白扣了。
  qy_lot_not_found: 'qy_lot_err_not_found',
  qy_lot_not_open: 'qy_lot_err_not_open',
  qy_lot_closing_soon: 'qy_lot_err_closing_soon',
  qy_lot_cap_reached: 'qy_lot_err_cap_reached',
  qy_lot_user_cap: 'qy_lot_err_user_cap',
  qy_lot_attempt_cap: 'qy_lot_err_attempt_cap',
  qy_lot_cooldown: 'qy_lot_err_cooldown',
  qy_lot_inviter_cap: 'qy_lot_err_inviter_cap',
  qy_lot_ip_cap: 'qy_lot_err_ip_cap',
  qy_lot_entry_in_flight: 'qy_lot_err_entry_in_flight',
  qy_lot_ineligible: 'qy_lot_err_ineligible',
  qy_lot_bad_option: 'qy_lot_err_bad_option',
  qy_lot_bad_amount: 'qy_lot_err_bad_amount',
  qy_lot_bad_request_id: 'qy_lot_err_bad_request_id',
  // 三个幂等 / 落定出口必须说成三句话：in_progress 是"上一次还没落定，别再点"，
  // idem_conflict 是"换了参数还用同一个请求号，刷新重来"，not_settled 是
  // "钱可能已经动了，既不能说成功也不能说失败，去记录里复核"。
  qy_lot_in_progress: 'qy_err_in_progress',
  qy_lot_idem_conflict: 'qy_lot_err_idem_conflict',
  qy_lot_not_settled: 'qy_lot_err_not_settled',
  // 扣费成功但已错过封盘：费用会自动退回。绝不能显示成泛化的"操作冲突"。
  qy_lot_entry_excluded: 'qy_lot_err_entry_excluded',
  qy_lot_insufficient_quota: 'qy_lot_err_insufficient_quota',
  qy_lot_user_unavailable: 'qy_lot_err_user_unavailable',
  qy_lot_quota_overflow: 'qy_lot_err_quota_overflow',
  qy_lot_eligibility_unavailable: 'qy_lot_err_eligibility_unavailable',
  // 管理端。
  qy_lot_status_conflict: 'qy_lot_err_status_conflict',
  qy_lot_result_locked: 'qy_lot_err_result_locked',
  qy_lot_prize_cap: 'qy_lot_err_prize_cap',
  qy_lot_active_cap: 'qy_lot_err_active_cap',
  qy_lot_spend_not_ready: 'qy_lot_err_spend_not_ready',
  qy_lot_payout_not_found: 'qy_lot_err_payout_not_found',
  qy_lot_payout_needs_manual: 'qy_lot_err_payout_needs_manual',
  qy_lot_bad_request: 'qy_lot_err_bad_request',
  qy_lot_proof_not_ready: 'qy_lot_err_proof_not_ready',
  qy_lot_proof_disabled: 'qy_lot_err_proof_disabled',
}

/**
 * qy 的统一错误对象。
 *
 * 之所以自建 Error 子类而不是直接抛 AxiosError：调用方需要的是"这属于哪类失败"
 * 而不是"HTTP 是几"。`code` 原样暴露给调用方，页面可以在 kind 之上再做细分。
 */
export class QyError extends Error {
  readonly kind: QyFailureKind
  /** 后端返回的业务 code，无则为 null。 */
  readonly code: string | null
  /** 后端返回的原始 message，无则为 null。不保证已翻译。 */
  readonly rawMessage: string | null
  /** HTTP 状态码，网络层失败时为 null。 */
  readonly status: number | null

  constructor(
    kind: QyFailureKind,
    code: string | null,
    rawMessage: string | null,
    status: number | null
  ) {
    super(rawMessage ?? code ?? kind)
    this.name = 'QyError'
    this.kind = kind
    this.code = code
    this.rawMessage = rawMessage
    this.status = status
  }

  /** 默认展示用的 i18n key（code 白名单优先，其次按 kind）。 */
  get i18nKey(): string {
    if (this.code != null && QY_ERROR_CODE_I18N[this.code] != null) {
      return QY_ERROR_CODE_I18N[this.code]
    }
    return KIND_I18N_KEY[this.kind]
  }

  /** 扩展或功能被关闭 —— 调用方应当隐藏入口而不是报错。 */
  get isHidden(): boolean {
    return this.kind === 'disabled' || this.kind === 'feature_off'
  }
}

export function isQyError(error: unknown): error is QyError {
  return error instanceof QyError
}

/**
 * 把任意错误翻译成可展示文案。
 *
 * 顺序：code 白名单 → kind 级文案 → 后端原始 message → 未知错误兜底。
 * 后端 message 排在 i18n 之后是刻意的：它是中文硬编码，无法随语言切换。
 */
export function qyErrorMessage(error: unknown, t: TFunction): string {
  if (!isQyError(error)) return t('qy_err_unknown')
  const known = error.code != null ? QY_ERROR_CODE_I18N[error.code] : undefined
  if (known != null) return t(known)
  if (error.kind === 'business') {
    return error.rawMessage ?? t('qy_err_unknown')
  }
  return t(KIND_I18N_KEY[error.kind])
}

// ───────────────────────────── 内部实现 ─────────────────────────────

function isEnvelope(value: unknown): value is QyEnvelope<unknown> {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as { success?: unknown }).success === 'boolean'
  )
}

type AxiosLikeError = {
  response?: { status?: number; data?: unknown }
}

function readCode(data: unknown): string | null {
  if (typeof data !== 'object' || data === null) return null
  const code = (data as { code?: unknown }).code
  return typeof code === 'string' && code !== '' ? code : null
}

function readMessage(data: unknown): string | null {
  if (typeof data !== 'object' || data === null) return null
  const message = (data as { message?: unknown }).message
  return typeof message === 'string' && message !== '' ? message : null
}

/** 按 HTTP 状态码归类。code 优先于状态码：后端的 404 有 disabled 与 feature_off 两种含义。 */
function kindFromStatus(
  status: number | null,
  code: string | null
): QyFailureKind {
  if (code === CODE_DISABLED) return 'disabled'
  if (code === CODE_FEATURE_OFF) return 'feature_off'
  if (code === CODE_UNAVAILABLE) return 'unavailable'
  switch (status) {
    // 路由未注册时请求落到上游 NoRoute，返回的是 `{"error":{...}}` 甚至 HTML。
    // 没有 code 的 404 一律当作"扩展未启用"，这是前端零痕迹隐藏的判定依据。
    case 404:
      return 'disabled'
    case 503:
      return 'unavailable'
    case 403:
      return 'forbidden'
    case 409:
      return 'conflict'
    case 429:
      return 'rate_limited'
    case 400:
      return 'invalid'
    default:
      break
  }
  if (status != null && status >= 500) return 'server'
  if (status != null && status >= 400) return 'invalid'
  return 'network'
}

function toQyError(error: unknown): QyError {
  if (isQyError(error)) return error
  const response = (error as AxiosLikeError)?.response
  const status = typeof response?.status === 'number' ? response.status : null
  const code = readCode(response?.data)
  return new QyError(
    kindFromStatus(status, code),
    code,
    readMessage(response?.data),
    status
  )
}

function unwrap<T>(data: unknown, status: number): T {
  if (!isEnvelope(data)) {
    // 拿到 200 但不是信封 —— 部署配了 FRONTEND_BASE_URL 重定向时会回 HTML。
    // 这依然是"扩展没接上"，按 disabled 处理，前端静默隐藏。
    throw new QyError('disabled', null, null, status)
  }
  if (!data.success) {
    throw new QyError(
      'business',
      data.code ?? null,
      data.message ?? null,
      status
    )
  }
  return data.data as T
}

/**
 * 所有 qy 请求共用的配置。
 *
 * `skipErrorHandler` / `skipBusinessError` 必须为 true：上游响应拦截器会在
 * `success === false` 时直接 `toast.error(后端原始 message)`，那串中文既无法
 * i18n 也无法按 kind 分流，还会在"扩展未启用"时糊用户一脸红色报错。
 */
const QY_BASE_CONFIG: ApiRequestConfig = {
  skipErrorHandler: true,
  skipBusinessError: true,
}

/**
 * 把 `responseType: 'blob'` 请求的失败还原成 {@link QyError}。
 *
 * axios 的 `responseType` 对**错误响应一视同仁**：接口回 410 + JSON 信封时，
 * `response.data` 拿到的仍然是一个 Blob，`readCode` 从它身上读不到任何 code，
 * 于是所有业务错误都会塌缩成按状态码归类的那一档 —— 一句"请求参数不合法"。
 *
 * 因此这里只做一件事：把 Blob 解回普通对象，再交还给 {@link toQyError}。
 * **刻意不在此处复写状态码→kind 的映射**，那一份必须只有 kindFromStatus 一处。
 */
export async function qyErrorFromBlobFailure(error: unknown): Promise<QyError> {
  const response = (error as AxiosLikeError)?.response
  if (!(response?.data instanceof Blob)) return toQyError(error)
  let parsed: unknown = null
  try {
    parsed = JSON.parse(await response.data.text())
  } catch {
    // 不是 JSON（比如反代回的 HTML 错误页）：当作没有 code，按状态码归类。
    parsed = null
  }
  return toQyError({ response: { status: response.status, data: parsed } })
}

/** GET。走 `api.get` 以继承上游的在途请求去重。 */
export async function qyGet<T>(
  path: string,
  params?: Record<string, unknown>,
  config?: ApiRequestConfig
): Promise<T> {
  try {
    const res = await api.get(`${QY_API_PREFIX}${path}`, {
      ...QY_BASE_CONFIG,
      params,
      ...config,
    })
    return unwrap<T>(res.data, res.status)
  } catch (error) {
    throw toQyError(error)
  }
}

/** 变更类请求（POST / PUT / DELETE）。 */
export async function qyMutate<T>(
  method: 'delete' | 'post' | 'put',
  path: string,
  body?: unknown,
  config?: ApiRequestConfig
): Promise<T> {
  try {
    const res = await api.request({
      ...QY_BASE_CONFIG,
      method,
      url: `${QY_API_PREFIX}${path}`,
      data: body,
      ...config,
    })
    return unwrap<T>(res.data, res.status)
  } catch (error) {
    throw toQyError(error)
  }
}

export function qyPost<T>(
  path: string,
  body?: unknown,
  config?: ApiRequestConfig
): Promise<T> {
  return qyMutate<T>('post', path, body, config)
}

export function qyPut<T>(
  path: string,
  body?: unknown,
  config?: ApiRequestConfig
): Promise<T> {
  return qyMutate<T>('put', path, body, config)
}

export function qyDelete<T>(
  path: string,
  body?: unknown,
  config?: ApiRequestConfig
): Promise<T> {
  return qyMutate<T>('delete', path, body, config)
}
