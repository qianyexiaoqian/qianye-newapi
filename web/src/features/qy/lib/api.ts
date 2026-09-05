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

  // 档位闸门（middleware/root_action.go）。上游大写格式、不带 qy_ 前缀。
  // 不登记就会塌成 qy_err_forbidden（"你没有执行该操作的权限"）——
  // 那句话与"这条路由你整条都到不了"一模一样，而这里唯一有用的下一步是
  // "这个页面你还能用，只有这一个动作要找超级管理员"。两者在界面上必须可分。
  ROOT_ACTION_REQUIRED: 'qy_err_root_action_required',
  // 提现模块（`qy_wd_*`）与它透传的安全验证码（`SECURITY_PROOF_*`）已随
  // D-14 整体删除：星屑不可提现，没有任何一条路由会再回这些 code。

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
  // 不登记的话这 18 个 code 会按 HTTP 状态码归类：409 → `qy_err_conflict`
  //（"该申请已被其他人处理"，与地址簿毫不相干）、400 → "请求参数不合法"。
  // 管理员既不知道是重复、还是少了 scheme，也不知道该改哪里。
  qy_apiaddr_name_required: 'qy_err_aa_name_required',
  qy_apiaddr_name_too_long: 'qy_err_aa_name_too_long',
  qy_apiaddr_remark_too_long: 'qy_err_aa_remark_too_long',
  qy_apiaddr_group_too_long: 'qy_err_aa_group_too_long',
  qy_apiaddr_group_invalid: 'qy_err_aa_group_invalid',
  qy_apiaddr_groups_too_many: 'qy_err_aa_groups_too_many',
  qy_apiaddr_groups_too_long: 'qy_err_aa_groups_too_long',
  qy_apiaddr_color_invalid: 'qy_err_aa_color_invalid',
  qy_apiaddr_surface_invalid: 'qy_err_aa_surface_invalid',
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

  // ── 星辉佣金管理端（qianye/modules/commission，D-15 从 git HEAD 恢复）──
  // 冲正与手工增减那几组 code 随账本一起回来。`qy_withdrawn_*`（已提现额度迁移）
  // **不**回来：`balances/withdrawn` 端点没有恢复，那一列现在叫「已入账」且只由
  // 自动入账任务写。
  qy_reason_required: 'qy_err_cm_reason_required',
  qy_clawback_failed: 'qy_err_cm_clawback_failed',
  // 手工增减佣金（api_admin_adjust.go）。over_reclaimable 是「这个数填大了，减不了
  // 这么多」，overflow 是「加得太多」；同一个弹窗改了金额又提交撞的是下面商城那段
  // 共用的 `qy_idem_key_conflict`。
  qy_adj_over_reclaimable: 'qy_err_adj_over_reclaimable',
  qy_adj_overflow: 'qy_err_adj_overflow',
  qy_adj_user_not_found: 'qy_err_adj_user_not_found',

  // ── 邀请关系管理端（qianye/modules/invite/api_admin_relation.go，D-14 前在
  //    commission 模块下）──
  //
  // 七个关系 code 要求管理员做的下一步完全不同：改一个人 / 先解绑 / 这条绑定本身
  // 不该做 / 刷新重来。合并成一句"参数有误"只会让人对着同一个必然失败的
  // 请求反复重试。契约写的是 `qy_inv_*` 前缀"照抄改前缀"，而这一组在后端源码里
  // 本来就叫 `qy_rel_*`（不带 cm）；两套都登记，后端落哪一套界面都认得。
  qy_rel_self_invite: 'qy_err_rel_self_invite',
  qy_rel_user_not_found: 'qy_err_rel_user_not_found',
  qy_rel_already_bound: 'qy_err_rel_already_bound',
  qy_rel_cycle: 'qy_err_rel_cycle',
  qy_rel_not_bound: 'qy_err_rel_not_bound',
  qy_rel_conflict: 'qy_err_rel_conflict',
  // 换绑专属：换成他现在这个上线。回 400 而不是当空操作回成功。
  qy_rel_same_inviter: 'qy_err_rel_same_inviter',
  // 「这一行不挂在任何邀请关系上」：对着没有邀请人的账号点「停止计返」。
  qy_rel_no_relation: 'qy_inv_err_no_relation',
  qy_inv_self_invite: 'qy_err_rel_self_invite',
  qy_inv_user_not_found: 'qy_err_rel_user_not_found',
  qy_inv_already_bound: 'qy_err_rel_already_bound',
  qy_inv_cycle: 'qy_err_rel_cycle',
  qy_inv_not_bound: 'qy_err_rel_not_bound',
  qy_inv_conflict: 'qy_err_rel_conflict',
  qy_inv_same_inviter: 'qy_err_rel_same_inviter',
  qy_inv_no_relation: 'qy_inv_err_no_relation',
  // 邀请返被合规门挡住（邀请人未确认支付合规声明时不发任何邀请返）。
  qy_inv_compliance_required: 'qy_err_sd_compliance_required',
  // 同一个请求号换了参数再提交。星屑手调、商城下单、转盘转动都复用这个 code，
  // 文案是不提「金额」的通用说法 ——
  // 「账本上执行的是上一次那份参数」这层意思仍然要保留，那是它与"操作冲突"
  // 的全部区别。
  qy_idem_key_conflict: 'qy_err_idem_key_conflict',

  // ── 星屑（qianye/modules/stardust）──
  // 五个 code 要求做的下一步完全不同：去赚 / 去合规页勾确认 / 换个在册的分组名 /
  // 改来源清单 / 把数填小。不登记的话 400 全塌成"请求参数不合法"。
  qy_sd_insufficient: 'qy_err_sd_insufficient',
  qy_sd_compliance_required: 'qy_err_sd_compliance_required',
  qy_sd_group_unknown: 'qy_err_sd_group_unknown',
  qy_sd_bad_source: 'qy_err_sd_bad_source',
  qy_sd_adjust_too_large: 'qy_err_sd_adjust_too_large',
  // 手调的目标用户查不到（400）：user_id 打错了一位，或账号已被硬删除。
  // 塌成"请求参数不合法"会让管理员去改金额与事由重试。
  // （手调还会回 `qy_sd_overflow` 与操作人判据的 `qy_self_dealing` /
  // `qy_target_not_manageable`，它们登记在下面商城那一段，两个模块共用。）
  qy_sd_user_not_found: 'qy_err_sd_user_not_found',

  // ── 商城（qianye/modules/mall）──
  qy_ml_sold_out: 'qy_err_ml_sold_out',
  qy_ml_limit: 'qy_err_ml_limit',
  qy_ml_off_sale: 'qy_err_ml_off_sale',
  // 套餐商品下单前预览过的动作（新开 / 续期 / 顶替）在这一刻变了：
  // 必须让用户重看一遍再下单，不能说成一句"操作冲突"。
  qy_ml_plan_state_changed: 'qy_err_ml_plan_state_changed',
  qy_ml_plan_needs_outbox: 'qy_err_ml_plan_needs_outbox',
  qy_ml_address_required: 'qy_err_ml_address_required',
  qy_ml_has_open_orders: 'qy_err_ml_has_open_orders',
  qy_ml_bad_status: 'qy_err_ml_bad_status',
  // 契约 §7 之外、后端 `qianye/modules/mall/errors.go` 实际会回的那几条。
  // not_found 是 404：不登记会被 kindFromStatus 当成「扩展未启用」静默隐藏，
  // 而它的真相是"这件商品 / 这张单已经不存在了"。
  qy_ml_not_found: 'qy_err_ml_not_found',
  // 一整族参数校验共用一个 code（标题超长 / 售价越界 / 售期倒挂 / 套餐不存在 …），
  // 后端那句话才是答案 —— 见下面 QY_SERVER_MESSAGE_CODES；静态回落用通用的
  // 「请求参数不合法」，它本来就是"后端没给 message"时唯一诚实的说法。
  qy_ml_bad_request: 'qy_err_invalid',
  // 收货地址已过保留期被清除（410）：不是权限问题，也不是"再试一次"能解决的。
  qy_ml_address_pruned: 'qy_err_ml_address_pruned',
  // 抽奖所得的实物奖品单补填地址（`POST /mall/orders/:no/address`）。三条各自的
  // 下一步不同：这张单不是奖品单（地址在下单时就填过了）/ 已经填过一次（改地址
  // 走工单）/ 管理端对没填地址的单点了发货（先等中奖者填）。不登记就塌成
  // 「权限不足」「操作冲突」，而这三句里没有一句在说"再试一次"。
  qy_ml_not_prize_order: 'qy_err_ml_not_prize_order',
  qy_ml_address_exists: 'qy_err_ml_address_exists',
  qy_ml_address_missing: 'qy_err_ml_address_missing',
  qy_ml_max_products: 'qy_err_ml_max_products',
  // 退款会把余额顶过系统上界：处置是"先花掉一部分"，与"星屑不足"方向相反。
  qy_sd_overflow: 'qy_err_sd_overflow',
  // 商品封面。七个 code 各自要求的下一步不同，与抽奖封面同一套理由。
  qy_ml_cover_required: 'qy_err_ml_cover_required',
  qy_ml_cover_too_large: 'qy_err_ml_cover_too_large',
  qy_ml_cover_type: 'qy_err_ml_cover_type',
  qy_ml_cover_not_found: 'qy_err_ml_cover_not_found',
  qy_ml_cover_purged: 'qy_err_ml_cover_purged',
  qy_ml_cover_pending_limit: 'qy_err_ml_cover_pending_limit',
  qy_ml_cover_store_failed: 'qy_err_ml_cover_store_failed',
  // 操作人判据的三个方向（403 / 404）。不登记就塌成一句「权限不足」，而这三条
  // 各自的下一步是：换一位管理员 / 找更高权限的人 / 先确认账号去向。
  // 星屑手调（qianye/modules/stardust）回的也是前两条，这里的文案刻意不提"订单"。
  qy_self_dealing: 'qy_err_self_dealing',
  qy_target_not_manageable: 'qy_err_target_not_manageable',
  qy_target_missing: 'qy_err_target_missing',

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
  qy_lot_ineligible: 'qy_lot_err_ineligible',
  qy_lot_bad_option: 'qy_lot_err_bad_option',
  qy_lot_bad_amount: 'qy_lot_err_bad_amount',
  qy_lot_bad_request_id: 'qy_lot_err_bad_request_id',
  // 期次池的两条**方向相反**的错误,必须各说各的。
  // pool_short 是「池子不够大」,处置是注资;pool_ceiling 是「池子已经太大」,
  // 而注资只会让它更糟(奖级配置也救不了,那条判据是无条件的)。
  // 不登记的话两条都回落成泛化的「参数不合法」,运营既看不出方向也看不出处置。
  qy_lot_series_pool_short: 'qy_lot_err_series_pool_short',
  qy_lot_series_pool_ceiling: 'qy_lot_err_series_pool_ceiling',
  // 一次买多注的三条。前两条是请求形状错了（前端写错才会发生），第三条是
  // **部分成交**：前面几注已经买成、后面几注被时间预算截断且一分钱没扣。
  // 第三条若回落成泛化的"操作冲突"，用户会以为整批都失败了而再提交一次。
  qy_lot_too_many_picks: 'qy_lot_err_too_many_picks',
  qy_lot_pick_conflict: 'qy_lot_err_pick_conflict',
  qy_lot_batch_budget: 'qy_lot_err_batch_budget',
  // 玩法被运营隐藏。文案必须自己说清"已参与的不受影响"—— 用户看到自己参加过的
  // 那一场突然不能再买，第一反应是自己那笔钱出事了，而这条错误在用户端与管理端
  // （发布被拦）共用同一句。
  qy_lot_play_hidden: 'qy_lot_err_play_hidden',
  // 三个幂等 / 落定出口必须说成三句话：in_progress 是"上一次还没落定，别再点"，
  // idem_conflict 是"换了参数还用同一个请求号，刷新重来"，not_settled 是
  // "钱可能已经动了，既不能说成功也不能说失败，去记录里复核"。
  qy_lot_idem_conflict: 'qy_lot_err_idem_conflict',
  // 扣费成功但已错过封盘：费用会自动退回。绝不能显示成泛化的"操作冲突"。
  qy_lot_insufficient_quota: 'qy_lot_err_insufficient_quota',
  qy_lot_eligibility_unavailable: 'qy_lot_err_eligibility_unavailable',
  // 管理端。
  qy_lot_status_conflict: 'qy_lot_err_status_conflict',
  qy_lot_result_locked: 'qy_lot_err_result_locked',
  qy_lot_prize_cap: 'qy_lot_err_prize_cap',
  // 净增发的两条。后端那两句话里带着**换算成站内余额之后的金额**，而这里的
  // `t(known)` 不接受插值参数 —— 所以这两句刻意不复述金额，只说下一步是什么：
  // 金额已经由创建向导的「本场最坏会发出 X」摆在同一屏上（NetIssueMeter），
  // 在这里再写一个没有数字的"金额过大"只会让人去找那个数在哪。
  qy_lot_net_issue_confirm: 'qy_lot_err_net_issue_confirm',
  qy_lot_net_issue_overflow: 'qy_lot_err_net_issue_overflow',
  qy_lot_active_cap: 'qy_lot_err_active_cap',
  qy_lot_spend_not_ready: 'qy_lot_err_spend_not_ready',
  qy_lot_payout_not_found: 'qy_lot_err_payout_not_found',
  // 「人工核对落账」的六个 code 已随端点一起删除（派奖改走星屑，扩展库单库事务，
  // 不再有"主库动没动钱不知道"的那一档）。
  //
  // 转盘（`draw_mode='wheel'`）。五条各说各的：关了 / 奖档在你转动的这一瞬被改了
  // （刷新重来）/ 客户端种子格式不对 / 列表过滤参数写错了（前端写错才会发生）/
  // 管理端对已封盘且有人转过的转盘点了「取消」（它只等揭示，什么都做不了）。
  // 最后一条尤其不能塌成"操作冲突"：那句话是"刷新一下再试"，而这里的真相是
  // "这件事从此不可能做到"。
  qy_lot_wheel_closed: 'qy_lot_err_wheel_closed',
  qy_lot_spec_drift: 'qy_lot_err_spec_drift',
  qy_lot_bad_client_seed: 'qy_lot_err_bad_client_seed',
  qy_lot_bad_draw_mode: 'qy_lot_err_bad_draw_mode',
  qy_lot_wheel_no_cancel: 'qy_lot_err_wheel_no_cancel',
  // 转盘改排期（`PUT …/schedule`）。两条都是"这件事从此做不到"，不是"刷新再试"：
  // 批次玩法的时刻进承诺原像、发布后永远不可改；转盘一旦封盘（到点 / 提前结束 /
  // 库存耗尽）名单已冻结、只等揭示，排期对它没有意义了。
  qy_lot_schedule_not_wheel: 'qy_lot_err_schedule_not_wheel',
  qy_lot_wheel_schedule_locked: 'qy_lot_err_wheel_schedule_locked',
  // 改标题 / 说明（`PUT …/basics`）：已结算或已结束的活动名字已随证据链公示，
  // 从此不可改 —— 不是"刷新再试"。
  qy_lot_basics_locked: 'qy_lot_err_basics_locked',
  // 只在后端**没给** message 时才用得上（见 QY_SERVER_MESSAGE_CODES）。
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
  // 「彻底删除」的五道硬闸门 + 两道前置。七个 code 全都是 409/400，不登记就会
  // 塌成一句"操作冲突"，而它们要求运营做的下一步完全不同：等出款落定 / 先去
  // 发那串兑换码 / 先处理对账异常 / 先关闭双色球系列再从最新一期往前删 /
  // 把编号原样敲一遍 / 去把审计打开。塌成一句话的后果是运营反复点同一个按钮。
  qy_lot_delete_not_finished: 'qy_lot_err_delete_not_finished',
  qy_lot_delete_funds_open: 'qy_lot_err_delete_funds_open',
  qy_lot_delete_text_pending: 'qy_lot_err_delete_text_pending',
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
  if (
    error.code != null &&
    QY_SERVER_MESSAGE_CODES.has(error.code) &&
    error.rawMessage != null &&
    error.rawMessage !== ''
  ) {
    return error.rawMessage
  }
  const known = error.code != null ? QY_ERROR_CODE_I18N[error.code] : undefined
  if (known != null) return t(known)
  if (error.kind === 'business') {
    return error.rawMessage ?? t('qy_err_unknown')
  }
  return t(KIND_I18N_KEY[error.kind])
}

/**
 * 「后端那句话就是答案」的 code。
 *
 * 绝大多数 code 表示的是一件确定的事（"这张单已经结过了"），前端有更好的说法，
 * 所以白名单里那一份静态译文更有用。但有一类 code 是**一整族**判据共用的：
 * `qy_lot_bad_request` 一个 code 背后是抽奖模块 96 处 `errBadRequest`，每一处都
 * 精心写着是哪一格错了、上限是多少、换算成站内余额是多少钱。
 *
 * 这类 code 走 `t(known)` 会把那句话整段丢掉，运营在向导上只看到一句
 * 「请求参数不合法」，然后去改别的字段。也别指望 kind 级文案兜底：400 归到
 * `invalid`，那句「请求参数有误，请检查后重试」信息量完全相同。
 *
 * 代价是这句话是中文硬编码、不随语言切换 —— 但一句看得懂的中文比一句
 * 翻译好了的废话有用，而且客户端校验（`qyLotValidateDraft`）已经把常见的
 * 二十来条拦在提交之前，走到这里的本来就是漏网的那几条。
 */
export const QY_SERVER_MESSAGE_CODES: ReadonlySet<string> = new Set([
  'qy_lot_bad_request',
  // 商城的同一族判据（`qianye/modules/mall/errors.go` 的 errBadRequest）：
  // 「标题必填且不超过 128 个字符」「plan_id 对应的套餐不存在」这类话只有后端
  // 说得出，前端替换成「请求参数不合法」会让运营去改别的字段。
  'qy_ml_bad_request',
])

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
