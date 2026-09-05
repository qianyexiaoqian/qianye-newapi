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
/*
 * 「邀请管理」选择夹（邀请关系 / 下线日消费 / 日结明细）在**真实渲染**上的形状（D-14）。
 *
 * # 这里守的三件事
 *
 *  1. 一进页面只打 `/admin/invite/relations`：下线日消费那一条是主库大表的聚合，
 *     不可见的标签一个请求都不能发。
 *  2. 「添加关系」走 `POST /admin/invite/relations/bind`，请求体恰好是
 *     `{invitee_id, inviter_id, reason}` 三个字段、id 是数字 —— 这是与后端之间
 *     唯一一份没有类型的契约。
 *  3. 日结明细把 `day` 与 `inviter_id` 带进请求，一行上同时印出基数（额度）、
 *     分组档 · 比例、计提（星屑）与状态徽章。
 */
import assert from 'node:assert/strict'
import { after, describe, test } from 'node:test'

import {
  ROLE,
  cleanupQyStardustScreens,
  inviteZh as zh,
  mountQyInviteScreen,
  setQyProbeRole,
  type QyProbeRequest,
} from '../../../__tests__/invite-screen'
import { qyTabHash } from '../../../lib/pages'
import { QyAdminInviteHub } from '../hub'

after(cleanupQyStardustScreens)

const RELATIONS = {
  items: [
    {
      invitee_id: 42,
      invitee_username: 'alice',
      invitee_resolved: true,
      inviter_id: 7,
      inviter_username: 'bob',
      inviter_resolved: true,
      bound_at: 1_787_000_000,
      invitee_created_at: 1_787_000_000,
      unbound_at: 0,
      total_stardust: 123,
      snapshot_present: true,
      blocked: false,
      risk_flags: '',
      block_reason: '',
    },
  ],
  total: 1,
  p: 1,
  page_size: 20,
}

const ACCRUALS = {
  items: [
    {
      inviter_id: 7,
      inviter_username: 'bob',
      invitee_id: 42,
      invitee_username: 'alice',
      bucket_date: '20260903',
      base_quota: 3_700_000,
      rate_group: 'vip',
      bps: 500,
      quota_per_unit: 500_000,
      gross: '0.3700000000',
      status: 'held',
      hold_reason: 'overdraft',
      ledger_no: '',
      created_at: 1_787_000_000,
      updated_at: 1_787_000_000,
    },
  ],
  total: 1,
  p: 1,
  page_size: 20,
}

function respond(request: QyProbeRequest) {
  if (request.url.endsWith('/admin/invite/relations')) {
    return { data: RELATIONS }
  }
  if (request.url.endsWith('/admin/invite/relations/bind')) {
    return { data: { invitee_id: 12, inviter_id: 7, bound: true } }
  }
  if (request.url.endsWith('/admin/invite/invite-accruals')) {
    return { data: ACCRUALS }
  }
  return undefined
}

async function mountHub(hash?: string) {
  await setQyProbeRole(ROLE.SUPER_ADMIN)
  return mountQyInviteScreen({
    element: <QyAdminInviteHub />,
    path: '/qy/admin/invite',
    initial: hash == null ? '/qy/admin/invite' : `/qy/admin/invite#${hash}`,
    features: { invite: true },
    respond,
  })
}

function qyUrls(sent: QyProbeRequest[]): string[] {
  return sent
    .filter((row) => row.url.startsWith('/api/qy/'))
    .map((row) => row.url)
}

describe('邀请关系标签', () => {
  test('一进页面只打 /admin/invite/relations；日消费那条大查询一次都不发', async () => {
    const screen = await mountHub()
    assert.deepEqual(qyUrls(screen.sent), ['/api/qy/admin/invite/relations'])
    const text = screen.text()
    assert.ok(text.includes('alice'), '下线没渲染')
    assert.ok(text.includes('bob'), '上线没渲染')
    assert.ok(text.includes('123 星屑'), '该关系累计返没按星屑渲染')
    // 四个动作都在同一行上：停止计返 / 换绑 / 解绑（新增绑定在筛选行）。
    for (const label of [
      zh.qy_inv_a_block,
      zh.qy_inv_a_rebind,
      zh.qy_rel_unbind,
    ]) {
      assert.ok(screen.text().includes(label), `缺按钮「${label}」`)
    }
    assert.ok(!text.includes('qy_inv_'), `有 qy_inv_ 键没翻译：${text}`)
  })

  test('添加关系：POST /admin/invite/relations/bind 的请求体恰好三个字段、id 是数字', async () => {
    const screen = await mountHub()
    assert.ok(await screen.click(zh.qy_rel_bind), '找不到「添加关系」')
    assert.ok(screen.text().includes(zh.qy_inv_a_bind_title), '绑定弹窗没打开')

    assert.ok(await screen.type(zh.qy_rel_invitee_id, '12'), '找不到下线 ID 框')
    assert.ok(await screen.type(zh.qy_rel_inviter_id, '7'), '找不到上线 ID 框')
    assert.ok(
      await screen.type(zh.qy_rel_reason, '工单 T-1 核实'),
      '找不到事由框'
    )

    screen.sent.length = 0
    assert.ok(await screen.click(zh.qy_rel_bind_submit), '找不到「确认绑定」')

    const posts = screen.sent.filter(
      (row) =>
        row.method === 'POST' &&
        row.url.endsWith('/admin/invite/relations/bind')
    )
    assert.equal(posts.length, 1, '应当恰好发一次 bind')
    assert.deepEqual(posts[0].body, {
      invitee_id: 12,
      inviter_id: 7,
      reason: '工单 T-1 核实',
    })
  })
})

describe('日结明细标签', () => {
  test('带 hash 打开只问日结那一条；day 与 inviter_id 进请求；一行四种口径各印各的', async () => {
    const screen = await mountHub(qyTabHash('/qy/admin/invite-accruals'))
    assert.deepEqual(
      qyUrls(screen.sent),
      ['/api/qy/admin/invite/invite-accruals'],
      '带 hash 打开时仍然先问了第一张标签'
    )
    const first = screen.sent.find((row) =>
      row.url.endsWith('/admin/invite/invite-accruals')
    )
    assert.ok(first != null)
    assert.match(String(first.params.day), /^\d{8}$/, 'day 必须是 YYYYMMDD')
    assert.equal(first.params.inviter_id, undefined, '没填邀请人时不该发空串')

    const text = screen.text()
    assert.ok(text.includes('2026-09-03'), '桶日没按 YYYY-MM-DD 渲染')
    assert.ok(text.includes('vip · 5%'), '分组档 · 比例没并排渲染')
    assert.ok(text.includes('0.3700000000 星屑'), '计提没带单位渲染')
    assert.ok(text.includes(zh.qy_sd_accrual_st_held), 'held 没翻译')
    assert.ok(text.includes(zh.qy_sd_hold_overdraft), '暂缓原因没写出来')
    assert.ok(!text.includes('3,700,000 星屑'), '消费基数被当成星屑渲染了')

    screen.sent.length = 0
    assert.ok(
      await screen.type(zh.qy_rel_inviter_id_ph, '7'),
      '找不到邀请人筛选框'
    )
    const asked = screen.sent.filter((row) =>
      row.url.endsWith('/admin/invite/invite-accruals')
    )
    assert.equal(asked.length, 1)
    assert.equal(asked[0].params.inviter_id, '7')
    assert.equal(asked[0].params.p, 1, '换筛选必须回到第一页')
  })
})
