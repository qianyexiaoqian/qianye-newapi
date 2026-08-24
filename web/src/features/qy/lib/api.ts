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

export const KIND_I18N_KEY: Record<QyFailureKind, string> = {
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
  // 安全验证（middleware/secure_verification.go）。这四个 code 是上游大写格式，
  // 不带 qy_ 前缀 —— 提现收款明文接口把它们透传给了 qy 信封的解析层，
  // 不登记就会塌成一句"权限不足"，而正确的下一步是"重新验证一次身份"。
  SECURITY_PROOF_REQUIRED: 'qy_err_security_proof',
  SECURITY_PROOF_INVALID: 'qy_err_security_proof',
  SECURITY_PROOF_EXPIRED: 'qy_err_security_proof',
  SECURITY_PROOF_SCOPE_MISMATCH: 'qy_err_security_proof',
  SECURITY_PROOF_METHOD_MISMATCH: 'qy_err_security_proof',
  // 档位闸门（middleware/root_action.go）。同样是上游大写格式、不带 qy_ 前缀。
  // 不登记就会塌成 qy_err_forbidden（"你没有执行该操作的权限"）——
  // 那句话与"这条路由你整条都到不了"一模一样，而这里唯一有用的下一步是
  // "这个页面你还能用，只有这一个动作要找超级管理员"。两者在界面上必须可分。
  ROOT_ACTION_REQUIRED: 'qy_err_root_action_required',
  qy_wd_payout_ref_required: 'qy_err_wd_payout_ref_required',
  qy_wd_payout_amount_required: 'qy_err_wd_payout_amount_required',
  qy_wd_payout_amount_mismatch: 'qy_err_wd_payout_amount_mismatch',
  qy_wd_user_unavailable: 'qy_err_wd_user_unavailable',
  qy_wd_insufficient_commission: 'qy_err_wd_insufficient',
  qy_wd_debt_blocked: 'qy_err_wd_debt_blocked',
  qy_wd_daily_count_reached: 'qy_err_wd_daily_count',
  qy_wd_fiat_below_min: 'qy_err_wd_fiat_below_min',
  qy_wd_fee_eats_all: 'qy_err_wd_fee_eats_all',
  qy_wd_fiat_unavailable: 'qy_err_wd_fiat_unavailable',
  qy_wd_not_found: 'qy_err_wd_not_found',
  qy_wd_status_conflict: 'qy_err_wd_status_conflict',
  // 本轮新加的两道越权闸门。不登记就会塌成一句 qy_err_forbidden：
  // 403 在 kindFromStatus 里被归成 'forbidden'，而 qyErrorMessage 只在
  // kind === 'business' 时才回落后端原始 message —— 后端精心写的
  // 「请由另一位管理员处理」/「请由更高权限的管理员处理」这两句**唯一**告诉
  // 管理员下一步该怎么办的话，一个字都到不了界面，而且与任何其它 403 不可区分。
  // 提现四个人工决定共用同一条渲染路径，列表页又没有任何预先标记，
  // 审核员只能挨个点、挨个吃通用报错。
  qy_wd_self_review: 'qy_err_wd_self_review',
  qy_wd_peer_review: 'qy_err_wd_peer_review',
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
  // AFF 关系绑定/解绑（qianye/modules/commission/api_admin_relation.go）。
  // 六个 code 要求管理员做的下一步完全不同：改一个人 / 先解绑 / 这条绑定本身
  // 不该做 / 刷新重来。合并成一句"参数有误"只会让人对着同一个必然失败的
  // 请求反复重试。
  qy_rel_self_invite: 'qy_err_rel_self_invite',
  qy_rel_user_not_found: 'qy_err_rel_user_not_found',
  qy_rel_already_bound: 'qy_err_rel_already_bound',
  qy_rel_cycle: 'qy_err_rel_cycle',
  qy_rel_not_bound: 'qy_err_rel_not_bound',
  qy_rel_conflict: 'qy_err_rel_conflict',
  // 换绑专属：换成他现在这个上线。回 400 而不是当空操作回成功，见
  // `adminRebindRelation` 的说明。
  qy_rel_same_inviter: 'qy_err_rel_same_inviter',
  // 「这一行不挂在任何邀请关系上」。项目方今天撞到的就是它：手工调整产生的
  // 计佣行 invitee_id = 0（那笔钱不是从谁的消费里分出来的），对着它点「拉黑」
  // 必然失败。此前后端与「报文格式错」共用 qy_invalid_param，运营看到的是
  // "请求参数有误"，于是会怀疑自己填错了、再点一次。
  //
  // 复用 `qy_cm_block_no_relation` 这句已有的文案而不是新造一个键：它本来就是
  // 为这一档写的（"没有任何数据被改动"那半句尤其要留着），此前挂在一个按 kind
  // 分档的本地函数上，现在后端给了独立 code，它终于能挂到 code 上。
  qy_rel_no_relation: 'qy_cm_block_no_relation',
  // 手工增减佣金（qianye/modules/commission/api_admin_adjust.go）。
  // over_reclaimable 是「这个数填大了，减不了这么多」，overflow 是「加得太多」，
  // idem_key_conflict 是「同一个弹窗改了金额又提交」—— 后者尤其不能说成
  // "操作冲突"：账本上执行的是上一次的金额，管理员必须知道这件事。
  qy_adj_over_reclaimable: 'qy_err_adj_over_reclaimable',
  qy_adj_overflow: 'qy_err_adj_overflow',
  qy_adj_user_not_found: 'qy_err_adj_user_not_found',
  qy_idem_key_conflict: 'qy_err_adj_idem_conflict',

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
  // 玩法被运营隐藏。文案必须自己说清"已参与的不受影响"—— 用户看到自己参加过的
  // 那一场突然不能再买，第一反应是自己那笔钱出事了，而这条错误在用户端与管理端
  // （发布被拦）共用同一句。
  qy_lot_play_hidden: 'qy_lot_err_play_hidden',
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
  // 人工核对落账的六个拒绝理由。不登记会全塌成后端那句中文原文，
  // 而它们要求运营做的下一步完全不同：改用「重试」 / 等补偿任务收敛 /
  // 刷新后重试 / 把核对依据填完。
  qy_lot_adjudicate_not_held: 'qy_lot_err_adjudicate_not_held',
  qy_lot_adjudicate_no_order: 'qy_lot_err_adjudicate_no_order',
  qy_lot_adjudicate_order_settled: 'qy_lot_err_adjudicate_order_settled',
  qy_lot_adjudicate_order_open: 'qy_lot_err_adjudicate_order_open',
  qy_lot_adjudicate_verdict: 'qy_lot_err_adjudicate_verdict',
  qy_lot_adjudicate_reason: 'qy_lot_err_adjudicate_reason',
  qy_lot_bad_request: 'qy_lot_err_bad_request',
  // 卡片背景图。十一个 code 各自要求的下一步完全不同：换一张更小的 / 换一种
  // 格式 / 先保存或移除待用的那几张 / 地址写错了 / 两种来源只能二选一 /
  // 刷新后重试。不登记就全塌成一句"请求参数不合法"，而运营会去改别的字段。
  qy_lot_cover_disabled: 'qy_lot_err_cover_disabled',
  qy_lot_cover_required: 'qy_lot_err_cover_required',
  qy_lot_cover_too_large: 'qy_lot_err_cover_too_large',
  qy_lot_cover_type: 'qy_lot_err_cover_type',
  qy_lot_cover_not_found: 'qy_lot_err_cover_not_found',
  qy_lot_cover_purged: 'qy_lot_err_cover_purged',
  qy_lot_cover_pending_limit: 'qy_lot_err_cover_pending_limit',
  qy_lot_cover_store_failed: 'qy_lot_err_cover_store_failed',
  qy_lot_cover_both_sources: 'qy_lot_err_cover_both_sources',
  qy_lot_cover_bad_url: 'qy_lot_err_cover_bad_url',
  qy_lot_cover_raced: 'qy_lot_err_cover_raced',
  qy_lot_proof_not_ready: 'qy_lot_err_proof_not_ready',
  qy_lot_proof_disabled: 'qy_lot_err_proof_disabled',
  // 「彻底删除」的六道硬闸门 + 两道前置。八个 code 全都是 409/400，不登记就会
  // 塌成一句"操作冲突"，而它们要求运营做的下一步完全不同：等出款落定 / 先去
  // 发那串兑换码 / 先处理对账异常 / 先关闭双色球系列再从最新一期往前删 /
  // 把编号原样敲一遍 / 去把审计打开。塌成一句话的后果是运营反复点同一个按钮。
  qy_lot_delete_not_finished: 'qy_lot_err_delete_not_finished',
  qy_lot_delete_funds_open: 'qy_lot_err_delete_funds_open',
  qy_lot_delete_text_pending: 'qy_lot_err_delete_text_pending',
  qy_lot_delete_entry_open: 'qy_lot_err_delete_entry_open',
  qy_lot_delete_flag_open: 'qy_lot_err_delete_flag_open',
  qy_lot_delete_series_live: 'qy_lot_err_delete_series_live',
  qy_lot_delete_evidence_broken: 'qy_lot_err_delete_evidence_broken',
  qy_lot_delete_confirm: 'qy_lot_err_delete_confirm',
  qy_lot_delete_audit_off: 'qy_lot_err_delete_audit_off',
  // 草稿那一支换成三条正向断言（不许有参与明细 / 出款 / 承诺痕迹），共用一个
  // code。它们在库结构正常时一条都不可能成立，所以它要求运营做的下一步只有
  // 一个：去查这份草稿上为什么会有那些行，而不是重试。
  qy_lot_delete_draft_dirty: 'qy_lot_err_delete_draft_dirty',
  // 改草稿被拒。**不能塌进 qy_lot_status_conflict**：那句话是"刷新一下再试"，
  // 而这里的真相是"发布之后这件事从此不可能做到"。塌进去的表现是运营反复
  // 刷新、反复重试一件永远不会成功的事。
  qy_lot_update_not_draft: 'qy_lot_err_update_not_draft',
  qy_lot_cancel_draft: 'qy_lot_err_cancel_draft',
  // 违规解封:409 走 kind='conflict',共享层不会透出后端原文,
  // 不登记就会退化成通用的「操作冲突」—— 而这两档要求运营做的下一步完全相反。
  qy_vio_no_pending_ban: 'qy_vio_err_no_pending_ban',
  qy_vio_ban_churning: 'qy_vio_err_ban_churning',
  // 下架（「关闭」）。not_finished 与另外两个的处置完全不同：前者是"这一场还没
  // 结束，你要的其实是取消"，后两个只是刷新一下就好。
  qy_lot_hide_not_finished: 'qy_lot_err_hide_not_finished',
  qy_lot_hide_already: 'qy_lot_err_hide_already',
  qy_lot_hide_not_hidden: 'qy_lot_err_hide_not_hidden',

  // ── 渠道批量操作（qianye/modules/channelops/errors.go）──
  // 五个整批级别的 code。不登记的话它们会按 HTTP 400 塌成一句"请求参数不合法"，
  // 而其中三个要求管理员做的下一步完全不同：先选渠道 / 分批再来 / 勾一个要清的项。
  qy_chops_no_ids: 'qy_err_chops_no_ids',
  qy_chops_too_many: 'qy_err_chops_too_many',
  qy_chops_bad_status: 'qy_err_chops_bad_status',
  qy_chops_nothing_to_reset: 'qy_err_chops_nothing_to_reset',
  qy_chops_invalid_param: 'qy_err_invalid',

  // ── 二开检查更新（qianye/controller/update_check.go）──
  //
  // 这六个 code **必须全部登记**，而且必须映射到六句不同的话。后端把失败拆成
  // 六档正是这个端点存在的理由（浏览器直连拿不到状态码，所有失败都塌成一个
  // TypeError）；前端漏登记的话它们会按 HTTP 状态码归类 —— 五个 502 一起变成
  // `qy_err_server`（"服务器出错了"），一个 429 变成 `qy_err_rate_limited`，
  // 后端那一层拆分就白做了，而管理员要做的下一步四者完全不同：
  // 查这台机器的出网 / 查仓库地址 / 等一小时 / 查出站 IP 是不是被封了。
  qy_update_unreachable: 'qy_err_update_unreachable',
  qy_update_rate_limited: 'qy_err_update_rate_limited',
  qy_update_source_missing: 'qy_err_update_source_missing',
  qy_update_forbidden: 'qy_err_update_forbidden',
  qy_update_unexpected_status: 'qy_err_update_unexpected_status',
  qy_update_bad_payload: 'qy_err_update_bad_payload',
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
  // 没有这一行,一个「发出去但永远收不到响应」的请求会永远挂着 —— XHR 的默认
  // timeout 是 0,也就是无限等。它不是理论风险,违规类型页整块塌成永久转圈就是
  // 它:那一个 promise 永不落定,于是 React Query 里 key 为
  // `qy/admin/violation/categories` 的 query 永远停在
  // status='pending' + fetchStatus='fetching'。这个状态是**自锁**的 ——
  // query-core 的 Query.fetch() 在 fetchStatus!=='idle' 且 retryer 未 rejected 时
  // 直接返回那条死掉的 promise、不再发请求(query.js:183-193),而
  // optionalRemove() 又要求 fetchStatus==='idle' 才回收(query.js:63-67),
  // 于是它连 GC 都躲过去,重新挂载、invalidate、refetch 全部无效,只有整页重载能救。
  //
  // 30s 的依据是「比任何 qy 接口的服务端上界都宽」:最慢的一个是 AI 渠道试跑,
  // 后端已经把它钉死在 15s(qianye/modules/violation/api_admin_aireview.go:308),
  // 其余都是个位数毫秒的管理端 CRUD。调用方仍可用 `config.timeout` 单独放宽。
  timeout: 30_000,
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

/** 变更类请求（POST / PUT / PATCH / DELETE）。 */
export async function qyMutate<T>(
  method: 'delete' | 'patch' | 'post' | 'put',
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

/**
 * PATCH —— **只改一部分字段**。
 *
 * 与 `qyPut` 的分工不是风格偏好：`qyPut` 提交的是前端手上那一整份对象，而那份
 * 对象往往是列表页十几秒前拉下来的拷贝。用它去翻一个开关，等于把这期间别人对
 * 其余字段的改动一起写回旧值 —— 一次没有人按下过的静默回滚。行内开关、单列状态
 * 变更这类「我只想改这一个」的动作走这里，后端也只 UPDATE 对应的那一列。
 */
export function qyPatch<T>(
  path: string,
  body?: unknown,
  config?: ApiRequestConfig
): Promise<T> {
  return qyMutate<T>('patch', path, body, config)
}

export function qyDelete<T>(
  path: string,
  body?: unknown,
  config?: ApiRequestConfig
): Promise<T> {
  return qyMutate<T>('delete', path, body, config)
}
