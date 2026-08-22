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
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { QY_ERROR_CODE_I18N } from '../../../lib/api'
import { QY_LOT_EMPTY_RULES } from '../../lottery/types'
import {
  qyLotDraftFromActivity,
  qyLotDraftToInput,
  qyLotValidateDraft,
} from '../lib/draft'
import type { QyLotAdminActivity } from '../types'

/**
 * 「草稿改不了删不了」的守卫。
 *
 * 项目方原话：「草稿活动为什么改不了删不了，你弄一下，超级管理员拥有最高
 * 权限的。」两条都不是权限问题：
 *
 *   · 改：`PUT /admin/lottery/activities/:act_no` 一直都在，**从来没有前端
 *     调用方**。这类缺陷零运行期症状 —— typecheck 全绿、单测全绿、接口
 *     照常存在，只是界面上点不到。与「找不到双色球」是同一种形状。
 *   · 删：后端第一道闸门要求 `status='finished'`，草稿被挡；界面上因此只有
 *     「取消活动」这一条路，而它会把草稿推进结算、写一条要对参与者公示的
 *     取消理由，留下一场 outcome=cancelled 的空活动。
 *
 * ## 这里守什么
 *
 *  1. 契约：`qyLotDraftFromActivity` 必须把**整份**草稿读回来 —— PUT 是整体
 *     替换语义，少带一个字段就是把那一列写成零值，而界面上没有任何提示；
 *  2. 源码：编辑与删除草稿的入口真的挂在详情页上，且删草稿用的是**另一个**
 *     弹窗（两档确认强度的差别全在那几句话里）；
 *  3. 文案：新增的两个后端 code 有登记、两种语言都有话说。
 */

const dir = dirname(fileURLToPath(import.meta.url))
const read = (...p: string[]) => readFileSync(join(dir, '..', ...p), 'utf8')

const detail = read('detail.tsx')
const wizard = read('components', 'lottery-create-wizard.tsx')
const draftDelete = read('components', 'lottery-draft-delete-dialog.tsx')
const api = read('api.ts')

const zh = JSON.parse(
  readFileSync(
    join(dir, '..', '..', '..', '..', '..', 'i18n', 'qy', 'zh.json'),
    'utf8'
  )
) as Record<string, string>
const en = JSON.parse(
  readFileSync(
    join(dir, '..', '..', '..', '..', '..', 'i18n', 'qy', 'en.json'),
    'utf8'
  )
) as Record<string, string>

/**
 * 一份"每一格都不是零值"的草稿活动行。
 *
 * 值刻意全部互不相同且非零：`qyLotDraftFromActivity` 漏掉任何一个字段的表现
 * 都是"读回来是 0 / 空串"，而如果夹具里那一格本来就是 0，这条守卫就成了空测。
 */
function draftActivity(): QyLotAdminActivity {
  return {
    act_no: 'LT20260822-abcdef',
    kind: 'guess',
    cover_url: 'https://example.invalid/a.png',
    cover_ref: '',
    draw_mode: '',
    series_no: '',
    issue_no: 0,
    status: 'draft',
    outcome: '',
    title: '一场竞猜',
    intro: '说明文字',
    stake_quota: 1000,
    open_at: 1_800_000_000,
    close_at: 1_800_003_600,
    draw_at: 1_800_007_200,
    settle_deadline: 1_800_086_400,
    commit_hash: '',
    rules_hash: 'rh',
    spec_hash: 'sh',
    algo: 'lot-v2',
    rules_text: JSON.stringify({
      min_quota: 12_345,
      min_account_age_days: 7,
      require_email: true,
      allow_groups: ['vip'],
      max_entries_per_user: 2,
      max_attempts_per_user: 5,
      max_total_entries: 300,
      max_total_users: 90,
      max_per_inviter: 4,
      cooldown_seconds: 45,
      dedup_ip: true,
    }),
    allow_multi_win: true,
    fee_bps: 700,
    min_entries_to_hold: 6,
    max_entries_per_user: 2,
    max_attempts_per_user: 5,
    max_total_entries: 300,
    max_total_users: 90,
    max_per_inviter: 4,
    cooldown_seconds: 45,
    dedup_ip: true,
    bet_min_quota: 500,
    bet_max_quota: 9_000,
    entry_seq: 0,
    active_count: 0,
    pending_count: 0,
    pool_quota: 0,
    platform_fee_quota: 0,
    payout_quota: 0,
    refund_quota: 0,
    roster_hash: '',
    roster_count: 0,
    chain_head: '',
    win_option_id: 0,
    result_evidence: '',
    result_by: 0,
    cancel_reason: '',
    created_by: 1,
    created_at: 1_799_000_000,
    published_at: 0,
    locked_at: 0,
    revealed_at: 0,
    settled_at: 0,
    hidden_at: 0,
    hidden_by: 0,
    hidden_reason: '',
  }
}

describe('草稿必须整份读得回表单', () => {
  test('一份竞猜草稿原样过一次「读回 → 提交」不丢任何字段', () => {
    const activity = draftActivity()
    const options = [
      { opt_no: 1, label: '甲赢', is_catch_all: false },
      { opt_no: 2, label: '乙赢', is_catch_all: false },
      { opt_no: 3, label: '以上都不是', is_catch_all: true },
    ]
    const input = qyLotDraftToInput(
      qyLotDraftFromActivity(activity, [], options)
    )

    // 顶层字段逐个对：PUT 是整体替换，少一个就是把那一列写成零值。
    assert.equal(input.kind, 'guess')
    assert.equal(input.title, '一场竞猜')
    assert.equal(input.intro, '说明文字')
    assert.equal(input.cover_url, 'https://example.invalid/a.png')
    assert.equal(input.cover_ref, '')
    assert.equal(input.stake_quota, 1000)
    assert.equal(input.open_at, activity.open_at)
    assert.equal(input.close_at, activity.close_at)
    assert.equal(input.draw_at, activity.draw_at)
    assert.equal(input.settle_deadline, activity.settle_deadline)
    assert.equal(input.allow_multi_win, true)
    assert.equal(input.fee_bps, 700)
    assert.equal(input.min_entries_to_hold, 6)
    // 单注上下限此前根本不在草稿里、提交时恒发 0 —— 一份配过上限的草稿
    // 被界面改一次就会被静默清零，而没有上限时一个大户可以在封盘前压满
    // 获胜选项吃掉整个奖池。
    assert.equal(input.bet_min_quota, 500)
    assert.equal(input.bet_max_quota, 9_000)

    // 频次那六格必须落在 `rules` 里面：后端顶层没有这些字段，
    // `ShouldBindJSON` 对未知字段是**静默丢弃**，发错位置不会报错，
    // 只会让整组风控显示"已设置"而实际一条都没生效。
    assert.equal(input.rules.max_entries_per_user, 2)
    assert.equal(input.rules.max_attempts_per_user, 5)
    assert.equal(input.rules.max_total_entries, 300)
    assert.equal(input.rules.max_total_users, 90)
    assert.equal(input.rules.max_per_inviter, 4)
    assert.equal(input.rules.cooldown_seconds, 45)
    assert.equal(input.rules.dedup_ip, true)
    // 分组/余额那一批条件同样进 rules_hash，一起带回来。
    assert.equal(input.rules.min_quota, 12_345)
    assert.equal(input.rules.min_account_age_days, 7)
    assert.equal(input.rules.require_email, true)
    assert.deepEqual(input.rules.allow_groups, ['vip'])

    assert.deepEqual(
      input.options,
      options.map((option) => ({ ...option }))
    )
    assert.deepEqual(input.prizes, [])
  })

  test('抽奖草稿的奖档整份带回来，包括表单上没有输入格的那两列', () => {
    const activity = { ...draftActivity(), kind: 'draw' as const, fee_bps: 0 }
    const prizes = [
      {
        tier: 1,
        name: '一等奖',
        amount_quota: 5000,
        count: 2,
        // 表单上没有 prize_type / text_desc 的输入格。它们仍然必须原样透传 ——
        // 否则一次"只改了标题"的保存会把一档文本奖悄悄变成额度奖。
        prize_type: 'text' as const,
        text_desc: '请在 8 月 31 日前联系客服领取',
      },
    ]
    const input = qyLotDraftToInput(
      qyLotDraftFromActivity(activity, prizes, [])
    )
    assert.equal(input.prizes.length, 1)
    assert.equal(input.prizes[0].prize_type, 'text')
    assert.equal(input.prizes[0].text_desc, '请在 8 月 31 日前联系客服领取')
    assert.equal(input.prizes[0].amount_quota, 5000)
    assert.equal(input.prizes[0].count, 2)
    assert.deepEqual(input.options, [])
  })

  test('rules_text 解析不了时，频次那六格仍然从活动行读得到', () => {
    // 后端新增一条参与条件、或者库里那份 JSON 被截断时，`parseQyLotRules`
    // 会回 null。此时退回"全部不限"只该影响分组/余额那一批，
    // 把频次闸门一起丢掉等于一次静默的风控降级。
    const activity = { ...draftActivity(), rules_text: '{ 这不是 JSON' }
    const draft = qyLotDraftFromActivity(activity, [], [])
    assert.equal(draft.max_entries_per_user, 2)
    assert.equal(draft.max_attempts_per_user, 5)
    assert.equal(draft.max_total_entries, 300)
    assert.equal(draft.max_total_users, 90)
    assert.equal(draft.cooldown_seconds, 45)
    assert.equal(draft.dedup_ip, true)
    assert.equal(draft.rules.max_per_inviter, 4)
    // 解析不出来的那一批如实回落成"不限"，而不是编一个值出来。
    assert.equal(draft.rules.min_quota, QY_LOT_EMPTY_RULES.min_quota)
  })

  test('单注上下限的两条校验：不能为负、下限不得大于上限', () => {
    const base = qyLotDraftFromActivity(
      draftActivity(),
      [],
      [
        { opt_no: 1, label: '甲', is_catch_all: false },
        { opt_no: 2, label: '乙', is_catch_all: true },
      ]
    )
    assert.deepEqual(
      qyLotValidateDraft(base, undefined, 0, 2000).filter((key) =>
        key.startsWith('qy_lot_v_bet')
      ),
      [],
      '一份合法的草稿不该报出单注上下限的错'
    )
    assert.deepEqual(
      qyLotValidateDraft(
        { ...base, bet_min_quota: -1 },
        undefined,
        0,
        2000
      ).filter((key) => key.startsWith('qy_lot_v_bet')),
      ['qy_lot_v_bet_negative']
    )
    assert.deepEqual(
      qyLotValidateDraft(
        { ...base, bet_min_quota: 9001, bet_max_quota: 9000 },
        undefined,
        0,
        2000
      ).filter((key) => key.startsWith('qy_lot_v_bet')),
      ['qy_lot_v_bet_order']
    )
    // 0 = 不限：只有上限非零时才比大小，否则"下限 500、上限不限"会被误拒。
    assert.deepEqual(
      qyLotValidateDraft(
        { ...base, bet_min_quota: 500, bet_max_quota: 0 },
        undefined,
        0,
        2000
      ).filter((key) => key.startsWith('qy_lot_v_bet')),
      []
    )
  })
})

describe('编辑草稿的入口真的在界面上', () => {
  test('详情页对草稿渲染「编辑草稿」按钮', () => {
    assert.ok(
      detail.includes("const isDraft = activity?.status === 'draft'"),
      '判据必须只有这一处 —— 抄第二份必然与后端那条 `WHERE status=draft` 漂移'
    )
    assert.ok(
      detail.includes("t('qy_lot_edit_title')"),
      '详情页上没有编辑入口 = PUT 接口仍然没有调用方，与改动之前一模一样'
    )
    assert.ok(
      detail.includes('QyLotActivityWizard'),
      '编辑必须复用创建那套四步表单：两份表单必然漂移'
    )
    assert.ok(
      detail.includes('qyLotDraftFromActivity'),
      '编辑表单的初值必须从活动详情整份重建 —— PUT 是整体替换语义'
    )
  })

  test('向导真的会打 PUT，而不是永远打 POST', () => {
    assert.ok(
      wizard.includes('updateQyLotActivity(editing.actNo'),
      '向导没有编辑分支 = 点了保存仍然在新建一场活动'
    )
    assert.ok(
      api.includes('export function updateQyLotActivity'),
      'api.ts 里没有 PUT 的封装'
    )
  })

  test('已发布的活动只能改封面，不能走这条编辑路', () => {
    // 后端的 `WHERE status='draft'` 是承诺不可篡改的唯一执行点，界面上
    // 不该出现一个点了必定吃 409 的按钮。
    assert.ok(
      detail.includes('const canEditDraft = isDraft'),
      '编辑入口必须挂在草稿判据上'
    )
    assert.ok(
      detail.includes("t('qy_lot_cover_change')"),
      '换封面是发布之后唯一还能改的东西，那个入口不能跟着一起消失'
    )
  })
})

describe('草稿的删除:确认强度与代价匹配', () => {
  test('草稿走的是另一个弹窗，两个入口并存', () => {
    assert.ok(
      detail.includes("t('qy_lot_draft_delete_title')"),
      '详情页上没有删草稿的入口 = 唯一处置仍然是「整场取消」'
    )
    assert.ok(
      detail.includes("t('qy_lot_delete_title')"),
      '已结束场次的强确认删除入口不许被顺手删掉'
    )
    assert.ok(detail.includes('QyLotDraftDeleteDialog'))
    assert.ok(detail.includes('QyLotDeleteDialog'))
  })

  test('草稿的确认框不要求回填活动编号', () => {
    assert.ok(
      !draftDelete.includes('confirm_act_no'),
      '给一个零代价的动作套上"请原样敲一遍编号"，只会训练运营对确认框整体失去敏感'
    )
    assert.ok(
      !draftDelete.includes("t('qy_lot_delete_warn_proof')"),
      '草稿删掉不会让任何一段证据链消失，列那四条代价是在撒谎'
    )
    assert.ok(
      draftDelete.includes("t('qy_lot_draft_delete_desc')"),
      '仍然要说清"为什么这次不用那么小心"，而不是一句「确定吗」'
    )
    assert.ok(
      draftDelete.includes("t('qy_lot_draft_delete_irreversible')"),
      '确认强度可以降，但"不可逆"这句话不能省'
    )
  })

  test('已结束那一档的强确认一个字节都没被放松', () => {
    const finished = read('components', 'lottery-delete-dialog.tsx')
    assert.ok(
      finished.includes(
        'const matched = confirmActNo.trim() === props.activity.act_no'
      ),
      '放开草稿最容易顺手带塌的就是这一条'
    )
    assert.ok(finished.includes('disabled={!matched ||'))
  })

  test('草稿的文案写明"没人见过它"，而不是含糊的"可以安全删除"', () => {
    assert.match(zh.qy_lot_draft_delete_desc, /404|不列|没公布|从没对外公布/)
    assert.match(zh.qy_lot_draft_delete_desc, /没有一分钱|收不到任何报名/)
    assert.match(zh.qy_lot_draft_delete_irreversible, /不可逆/)
    assert.match(zh.qy_lot_draft_delete_irreversible, /审计/)
    assert.match(en.qy_lot_draft_delete_desc, /never published/i)
    assert.match(en.qy_lot_draft_delete_irreversible, /irreversible/i)
  })
})

describe('草稿不该出现「整场取消」这个按钮', () => {
  test('canCancel 判据里明确排除了草稿', () => {
    /*
      取消曾经是草稿唯一的处置路径。「编辑草稿」「删除草稿」都做出来之后，
      它留在草稿上只剩伤害：一份从没对任何人公布过的活动被取消之后会变成
      finished/cancelled，于是永久出现在用户端大厅的「已结束」里，匿名证据链
      开始下发它的规则原文与**随机种子**（而 commit_hash 恒为空串，第三方按它
      验一次必然 FAIL），而且它从此不再是草稿、零仪式的草稿删除对它失效。
      换来的止损是零 —— 草稿上不可能有参与、扣款或要退的钱。

      后端已经硬拒（errCancelDraft）。前端这一条守的是「按钮压根不该出现」：
      只靠后端拒的话，运营会在同一排按钮上看到一个点了必然报错的红按钮。
    */
    const cancelDecl = detail.slice(
      detail.indexOf('const canCancel ='),
      detail.indexOf('const isFinished =')
    )
    assert.ok(cancelDecl.length > 0, '找不到 canCancel 的声明')
    assert.match(
      cancelDecl,
      /!isDraft/,
      'canCancel 必须排除草稿，否则草稿详情页上会渲染一个点了必然 409 的取消按钮'
    )
  })

  test('后端新 code 有登记、两种语言都有话说，并且不与「刷新后重试」同义', () => {
    const key = QY_ERROR_CODE_I18N.qy_lot_cancel_draft
    assert.ok(key != null, 'qy_lot_cancel_draft 没有登记进 QY_ERROR_CODE_I18N')
    assert.ok(zh[key] != null, '缺少中文文案')
    assert.ok(en[key] != null, '缺少英文文案')
    assert.notEqual(key, QY_ERROR_CODE_I18N.qy_lot_status_conflict)
    // 文案必须指向真正该点的那两个按钮，而不是稍后重试。
    assert.match(zh[key], /编辑草稿/)
    assert.match(zh[key], /删除草稿/)
  })
})

describe('新增的两个后端 code 都能变成人话', () => {
  test('登记进映射表，且两种语言都有文案', () => {
    for (const code of [
      'qy_lot_delete_draft_dirty',
      'qy_lot_update_not_draft',
    ]) {
      const key = QY_ERROR_CODE_I18N[code]
      assert.ok(key != null, `${code} 没有登记进 QY_ERROR_CODE_I18N`)
      assert.ok(zh[key] != null, `缺少 ${key} 的中文文案`)
      assert.ok(en[key] != null, `缺少 ${key} 的英文文案`)
    }
  })

  test('「只有草稿能改」不能塌成「刷新后重试」', () => {
    // 两句话要求运营做的下一步完全不同：前者在说"这件事从此不可能做到"，
    // 后者在说"再试一次"。塌成后者的表现是反复刷新、反复重试。
    assert.notEqual(
      QY_ERROR_CODE_I18N.qy_lot_update_not_draft,
      QY_ERROR_CODE_I18N.qy_lot_status_conflict
    )
    assert.match(zh.qy_lot_err_update_not_draft, /承诺哈希/)
    assert.match(zh.qy_lot_err_update_not_draft, /封面/)
  })

  test('「只有已结束的活动才能删除」这句话已经跟着改口径了', () => {
    // 后端放开草稿之后这句话就不再成立了。文案与判据脱节的表现是：
    // 运营删一份草稿成功了，却在别处读到"只有已结束的活动才能删除"。
    assert.match(zh.qy_lot_err_delete_not_finished, /草稿/)
    assert.match(en.qy_lot_err_delete_not_finished, /draft/i)
  })
})
