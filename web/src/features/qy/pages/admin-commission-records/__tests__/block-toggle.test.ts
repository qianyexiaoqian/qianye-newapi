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

import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * 「停止计佣」是不是一个可逆开关 —— 两页一致，而且界面说得出它与「解绑」的区别。
 *
 * ── 这批断言存在的理由 ──
 * 项目方看着计佣流水页问：「佣金审核这里是不是有点多余？停止计佣去把这个人的
 * aff 关系解绑不就好了？而且停止计佣就没有办法恢复计算了。」
 *
 * 他的结论对**这一页**成立：`admin-commission-records` 此前写死
 * `blocked: true`，本页只能停、不能恢复；而用户佣金页的下钻早就有切换。
 * 同一个后端接口（`relations/block` 收 `blocked bool`）、两页两种行为，
 * 运营看哪一页决定了他认为这个功能能不能用。
 *
 * 所以这里钉三件事：
 *   1. 两页的按钮都按**当前状态**切换，不再有写死的方向；
 *   2. 当前状态真的从后端来（`relation_blocked`），前端不拿"点过一次"猜；
 *   3. 「停止计佣」与「解绑」的区别写在确认框里，而且文案与后端的实际行为
 *      一致 —— 尤其是"停止期间的消费不补算"这一条。
 *
 * 判据落在**源码文本**上而不是渲染结果：这几条全是接线，渲染测试反而看不见
 * "方向是写死的"这种形状。
 */

// __tests__ → admin-commission-records → pages → qy → features → src → web → 仓库根
const repoRoot = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..',
  '..',
  '..'
)
const webSrc = join(repoRoot, 'web', 'src')
const commissionDir = join(repoRoot, 'qianye', 'modules', 'commission')

const qyPages = join(webSrc, 'features', 'qy', 'pages')
const recordsPage = readFileSync(
  join(qyPages, 'admin-commission-records', 'index.tsx'),
  'utf8'
)
const drilldown = readFileSync(
  join(
    qyPages,
    'admin-commission-users',
    'components',
    'user-commission-drilldown.tsx'
  ),
  'utf8'
)
const blockDialog = readFileSync(
  join(qyPages, 'admin-commission', 'components', 'block-relation-dialog.tsx'),
  'utf8'
)
const unbindDialog = readFileSync(
  join(
    qyPages,
    'admin-commission-relations',
    'components',
    'unbind-relation-dialog.tsx'
  ),
  'utf8'
)
const relationsPage = readFileSync(
  join(qyPages, 'admin-commission-relations', 'index.tsx'),
  'utf8'
)
const relationTypes = readFileSync(
  join(qyPages, 'admin-commission-relations', 'types.ts'),
  'utf8'
)
const accrualTypes = readFileSync(
  join(qyPages, 'admin-commission', 'types.ts'),
  'utf8'
)
const adminBackend = readFileSync(join(commissionDir, 'api_admin.go'), 'utf8')
const hookBackend = readFileSync(join(commissionDir, 'hook.go'), 'utf8')

const zhKeys = zh as Record<string, string>
const enKeys = en as Record<string, string>

describe('停止计佣的恢复入口', () => {
  test('计佣流水页不再写死 blocked: true', () => {
    // 这一行就是项目方那句"没有办法恢复"的全部来源。
    // 注释里会引用这个形状（说明它为什么被改掉），判据只看代码。
    const code = recordsPage.replace(/\/\*[\s\S]*?\*\//g, '')
    assert.ok(
      !/blocked:\s*true/.test(code),
      '计佣流水页又出现了写死的 blocked: true —— 本页就此只能停、不能恢复'
    )
  })

  test('两页的按钮都按当前状态切换', () => {
    assert.ok(
      recordsPage.includes(
        "row.relation_blocked ? t('qy_cm_unblock') : t('qy_cm_block')"
      ),
      '计佣流水页的按钮不再随关系当前状态切换'
    )
    assert.ok(
      drilldown.includes("row.blocked ? t('qy_cm_unblock') : t('qy_cm_block')"),
      '用户佣金下钻的按钮不再随关系当前状态切换'
    )
  })

  test('当前状态由后端下发，不是前端猜的', () => {
    assert.ok(
      adminBackend.includes('json:"relation_blocked"'),
      '计佣流水接口不再下发 relation_blocked：前端只能画一个单向按钮'
    )
    assert.ok(
      adminBackend.includes('Where("blocked = ? AND invitee_id IN ?"'),
      '后端不再按本页的 invitee_id 现查关系状态'
    )
    assert.ok(
      !adminBackend.includes('blocked := blockedInvitees(ctx)'),
      '列表改用了 blockedInvitees() 的 60 秒缓存：运营点完恢复，列表会在整个 TTL 里显示成没生效'
    )
    assert.ok(
      accrualTypes.includes('relation_blocked: boolean'),
      '前端 DTO 少了 relation_blocked'
    )
  })

  test('两页共用同一个确认框，语义只写一份', () => {
    for (const [name, source] of [
      ['计佣流水页', recordsPage],
      ['用户佣金下钻', drilldown],
    ] as const) {
      assert.ok(
        source.includes('BlockRelationDialog'),
        `${name}没有挂确认框：动作还是一点就走，运营看不到它与解绑的区别`
      )
    }
  })

  test('手工调整那类行仍然不渲染这个按钮', () => {
    // invitee_id = 0 的计佣行不挂在任何关系上，后端对它直接 400。
    assert.ok(
      recordsPage.includes('row.invitee_id > 0 ?'),
      '按钮又变成无条件渲染：手工调整行上会出现一个点了必然报错的按钮'
    )
    assert.ok(recordsPage.includes("t('qy_cm_block_na')"))
  })
})

describe('界面说得出「停止计佣」与「解绑」的区别', () => {
  test('确认框同时给出两个方向的语义与两者的区别', () => {
    for (const key of [
      'qy_cm_block_semantics',
      'qy_cm_unblock_semantics',
      'qy_cm_block_vs_unbind',
    ]) {
      assert.ok(
        blockDialog.includes(`'${key}'`),
        `确认框不再显示 ${key}：这个动作又变回一个只有名字的按钮`
      )
    }
  })

  test('解绑弹窗指回「停止计佣」这条可逆的路', () => {
    assert.ok(
      unbindDialog.includes("t('qy_rel_unbind_vs_block')"),
      '解绑弹窗不再提可逆的那条路：运营多数时候要的是暂停，却只会看到解绑'
    )
  })

  test('文案与后端实际行为一致 —— 停止期间的消费不补算', () => {
    // 后端行为：命中 blocked 直接 return，不留任何行；恢复只对之后的消费生效。
    // 两处判断（消费路径与充值/兑换码路径）都在，文案才敢说"不再产生新佣金"。
    assert.equal(
      (hookBackend.match(/blockedInvitees\(ctx\)/g) ?? []).length,
      2,
      '计佣路径上的 blocked 判断少了一处：某一条来源会在停止期间继续发佣金'
    )
    assert.ok(
      zhKeys['qy_cm_unblock_semantics'].includes('不补算'),
      '恢复计佣的文案必须说明停止期间那段不补算，否则运营会以为恢复=追认'
    )
    assert.ok(
      zhKeys['qy_cm_block_semantics'].includes('冲正'),
      '停止计佣的文案必须指出"已发的钱要收回请走冲正"'
    )
    assert.ok(
      zhKeys['qy_cm_block_vs_unbind'].includes('解绑'),
      '这一句的全部作用就是把两个动作摆在一起对比'
    )
  })
})

describe('i18n 键完整', () => {
  const used = [
    'qy_cm_block',
    'qy_cm_unblock',
    'qy_cm_block_na',
    'qy_cm_block_ok',
    'qy_cm_unblock_ok',
    'qy_cm_block_title',
    'qy_cm_unblock_title',
    'qy_cm_block_target',
    'qy_cm_block_semantics',
    'qy_cm_unblock_semantics',
    'qy_cm_block_vs_unbind',
    'qy_cm_block_reason',
    'qy_cm_block_reason_ph',
    'qy_cm_block_reason_hint',
    'qy_cm_block_submit',
    'qy_cm_unblock_submit',
    'qy_cm_block_default_reason',
    'qy_cm_unblock_default_reason',
    'qy_rel_state_blocked',
    'qy_rel_unbind_vs_block',
  ]

  test('本页用到的每个键在 en 与 zh 里都有', () => {
    for (const key of used) {
      assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
      assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
    }
  })

  test('带插值的文案两侧占位符一致', () => {
    for (const key of used) {
      const zhVars = [...zhKeys[key].matchAll(/\{\{(\w+)\}\}/g)]
        .map((m) => m[1])
        .sort()
      const enVars = [...enKeys[key].matchAll(/\{\{(\w+)\}\}/g)]
        .map((m) => m[1])
        .sort()
      assert.deepEqual(zhVars, enVars, `${key} 的插值变量两侧不一致`)
    }
  })

  test('两个默认事由的方向不能是同一句', () => {
    // 共用一句的后果：恢复计佣的审计正文写着"手动停止计佣"，
    // 而审计正文是事后仲裁"这一刻到底发生了什么"的唯一凭据。
    assert.notEqual(
      zhKeys['qy_cm_block_default_reason'],
      zhKeys['qy_cm_unblock_default_reason']
    )
    assert.ok(
      blockDialog.includes("'qy_cm_unblock_default_reason'"),
      '确认框没有按方向选默认事由'
    )
  })
})

/**
 * AFF 关系页是第三个、也是最该有这个开关的地方 —— 「解绑」按钮就在旁边。
 *
 * 在补上之前，这一页只有解绑:一个想"先停一下看看"的运营在这里唯一能按的
 * 就是那个不可逆的按钮。项目方"停止计佣不如直接解绑"的结论，正是从这种
 * 只摆一条路的页面上长出来的。
 */
describe('AFF 关系页的停止 / 恢复入口', () => {
  test('按钮按当前状态切换，且不写死方向', () => {
    assert.ok(
      relationsPage.includes(
        "row.blocked ? t('qy_cm_unblock') : t('qy_cm_block')"
      ),
      'AFF 关系页的停止/恢复按钮不随关系当前状态切换'
    )
    const code = relationsPage.replace(/\/\*[\s\S]*?\*\//g, '')
    assert.ok(
      !/blocked:\s*true/.test(code),
      'AFF 关系页出现了写死的 blocked: true —— 又变成只能停不能恢复'
    )
  })

  test('与另外两页共用同一个确认框', () => {
    // 判据要看**渲染点**而不是 import：只留 import 而不渲染，
    // 页面上什么都不会出现，而 includes('BlockRelationDialog') 照样为真。
    assert.ok(
      relationsPage.includes('<BlockRelationDialog'),
      'AFF 关系页没有挂确认框：这一页的解绑按钮就在旁边，不解释区别等于把人推向解绑'
    )
  })

  test('解绑入口仍然在，两条路并排摆着', () => {
    // 砍掉任何一个都会让另一个被当成万能解 —— 停止计佣不可逆地清关系，
    // 或者解绑变成"暂时停一下"的默认动作。
    assert.ok(relationsPage.includes("t('qy_rel_unbind')"))
  })
})

/**
 * 自动风控标记与人工事由必须分成两列、两格显示。
 *
 * 后端 `setRelationBlocked` 原来把管理员填的事由写进 `risk_flags`，而那一列是
 * `ensureRelation` 写自动判定（`reciprocal_invite`：互邀环路）的地方。于是一次
 * 人工停/恢复就把"系统判定这条关系是互刷"这个事实覆盖成一句人话，界面上那个
 * 徽标从此显示的是运营自己写的字，没有任何报错。
 */
describe('自动风控标记不被人工事由顶掉', () => {
  test('后端把人工事由写进 block_reason 而不是 risk_flags', () => {
    assert.ok(
      adminBackend.includes('"block_reason": truncate(reason, 255)'),
      'setRelationBlocked 不再写 block_reason'
    )
    assert.ok(
      !adminBackend.includes('"risk_flags": truncate(reason, 255)'),
      'setRelationBlocked 又把人工事由写回 risk_flags：自动风控标记会被一次停/恢复抹掉'
    )
  })

  test('前端 DTO 与列表把两者分开', () => {
    assert.ok(
      relationTypes.includes('block_reason: string'),
      '关系 DTO 少了 block_reason'
    )
    assert.ok(
      relationsPage.includes("row.risk_flags !== ''"),
      '关系页不再单独显示自动风控标记'
    )
    assert.ok(
      relationsPage.includes("row.block_reason !== ''"),
      '关系页不显示人工事由：那这一列写进去也没人看得到'
    )
  })

  test("人工事由用 !== '' 判定而不是真值判断", () => {
    // 与本模块其余可空字段同一个形状问题：真值判断在这里恰好也能跑，
    // 但它把"判据是不是空串"变成"判据是不是假值"，下一个字段就会踩空。
    assert.ok(
      !/row\.block_reason\s*&&/.test(relationsPage),
      '人工事由改用了真值判断'
    )
  })

  test('徽标文案两侧齐全且占位符一致', () => {
    const key = 'qy_rel_block_reason_badge'
    assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
    assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
    assert.ok(zhKeys[key].includes('{{reason}}'))
    assert.ok(enKeys[key].includes('{{reason}}'))
  })
})
