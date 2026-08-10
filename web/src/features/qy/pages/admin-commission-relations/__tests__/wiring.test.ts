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
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { qyPageMeta } from '@/features/qy/lib/page-meta'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

import { QY_FUND_REASON_MIN_RUNES } from '../../lib/constants'
import { QY_RELATION_SCOPES, QY_RELATION_SORTS } from '../types'

/**
 * AFF 关系页与手工增减佣金的接线断言。
 *
 * 这两块的真正风险不在渲染，而在四条**只会在运行时才现形**的链路：
 *
 *  1. 断链：关系页没有独立侧栏入口（`qy-settlement` 分组已占满 7 行），
 *     唯一入口是佣金审核页上的那个按钮。删掉它整页就是死链，而单测全绿；
 *  2. 同一概念的第 N 份拷贝：排序键、scope 键、事由长度下限在前后端各写一份，
 *     后端改了前端没有任何反应 —— 用户看到的是"排序选了没用"和"填了 4 个字还被拒"；
 *  3. 后端业务 code 没登记进 i18n 白名单：用户会看到后端的中文原文，
 *     而这几个 code 恰好要求管理员做完全不同的下一步（换个人 / 先解绑 / 别做）；
 *  4. i18n 键缺失：`t()` 找不到键时原样吐出键名，页面上会出现
 *     `qy_rel_unbind_semantics` 这种字符串 —— 而那一句正是解绑语义本身。
 */

// __tests__ → admin-commission-relations → pages → qy → features → src → web → 仓库根
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

const recordsPage = readFileSync(
  join(
    webSrc,
    'features',
    'qy',
    'pages',
    'admin-commission-records',
    'index.tsx'
  ),
  'utf8'
)
const routeFile = join(
  webSrc,
  'routes',
  '_authenticated',
  'qy',
  'admin',
  'commission-records',
  'relations',
  'index.tsx'
)
const relationBackend = readFileSync(
  join(commissionDir, 'api_admin_relation.go'),
  'utf8'
)
const adjustBackend = readFileSync(
  join(commissionDir, 'api_admin_adjust.go'),
  'utf8'
)
const qyApi = readFileSync(
  join(webSrc, 'features', 'qy', 'lib', 'api.ts'),
  'utf8'
)

const RELATIONS_URL = '/qy/admin/commission-records/relations'
const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

describe('AFF 关系页的入口链路', () => {
  test('佣金审核页上必须有指向本页的链接 —— 那是它唯一的入口', () => {
    assert.ok(
      recordsPage.includes(`to='${RELATIONS_URL}'`),
      `佣金审核页不再链向 ${RELATIONS_URL}：本页没有侧栏入口，删掉这个按钮就等于把整页变成死链`
    )
  })

  test('路由文件存在且挂的是本页组件', () => {
    assert.ok(existsSync(routeFile), `缺少路由文件 ${routeFile}`)
    const source = readFileSync(routeFile, 'utf8')
    assert.ok(
      source.includes('QyAdminCommissionRelations'),
      '路由文件没有挂载 QyAdminCommissionRelations'
    )
    assert.ok(
      source.includes(
        "'/_authenticated/qy/admin/commission-records/relations/'"
      ),
      '路由 id 与目录结构不一致，生成的 routeTree 会指向别处'
    )
  })

  test('URL 嵌在 commission-records 之下，因此继承佣金审核的 LAB MEMO 编号', () => {
    const meta = qyPageMeta(RELATIONS_URL)
    assert.notEqual(
      meta.no,
      '00',
      '本页脱离了 commission-records 前缀，区段头会退化成 LAB MEMO — 00'
    )
    assert.equal(meta.no, qyPageMeta('/qy/admin/commission-records').no)
  })

  test('手工增减佣金的入口挂在佣金余额页上', () => {
    const balancesPage = readFileSync(
      join(
        webSrc,
        'features',
        'qy',
        'pages',
        'admin-commission-balances',
        'index.tsx'
      ),
      'utf8'
    )
    assert.ok(
      balancesPage.includes('AdjustCommissionDialog'),
      '佣金余额页不再挂手工增减弹窗，那个接口就没有任何界面入口了'
    )
    assert.ok(balancesPage.includes("t('qy_adj_action')"))
  })
})

describe('前后端口径一致', () => {
  test('排序键与后端 relationSortOrders / unboundSortOrders 逐个对应', () => {
    // 后端拿不到白名单里的键就静默回落默认排序：前端多一个键的表现是
    // "选了没反应"，少一个键的表现是"后端支持的排序用不到"。两张表都要钉，
    // 因为它们作用在两个不同的数据库上，最容易各自漂移。
    for (const decl of ['relationSortOrders', 'unboundSortOrders']) {
      const block = relationBackend.match(
        new RegExp(`var ${decl} = map\\[string\\]string\\{([\\s\\S]*?)\\n\\}`)
      )
      assert.ok(block != null, `后端 ${decl} 声明找不到了`)
      const backendSorts = [...block[1].matchAll(/"([a-z_]+)":/g)].map(
        (m) => m[1]
      )
      assert.deepEqual(
        [...QY_RELATION_SORTS].sort(),
        backendSorts.sort(),
        `${decl} 与前端 QY_RELATION_SORTS 不一致`
      )
    }
  })

  test('scope 取值与后端分支逐字一致', () => {
    assert.ok(
      relationBackend.includes(`c.Query("scope") == "unbound"`),
      '后端不再按 scope=unbound 分流，前端那个下拉就变成了摆设'
    )
    assert.deepEqual([...QY_RELATION_SCOPES], ['bound', 'unbound'])
  })

  test('事由长度下限与后端 requireReason 一致', () => {
    assert.ok(
      relationBackend.includes(
        `if len([]rune(reason)) < ${QY_FUND_REASON_MIN_RUNES} {`
      ),
      '后端 requireReason 的下限变了，前端 QY_FUND_REASON_MIN_RUNES 必须跟着改，否则按钮放行、后端 400'
    )
  })

  test('手工调整必须带 client_request_id，前端必须真的生成一个', () => {
    // 语义是**增量**，没有幂等键时一次网络重试就是第二笔，而多发出去的佣金
    // 没有任何自动路径能收回来。
    assert.ok(
      adjustBackend.includes('缺少 client_request_id'),
      '后端不再要求幂等键了？那是一次网络重试就多发一笔的形状'
    )
    const dialog = readFileSync(
      join(
        webSrc,
        'features',
        'qy',
        'pages',
        'admin-commission-balances',
        'components',
        'adjust-commission-dialog.tsx'
      ),
      'utf8'
    )
    assert.ok(
      dialog.includes('crypto.randomUUID()'),
      '弹窗不再生成幂等键，后端会直接 400'
    )
    assert.ok(
      dialog.includes('client_request_id: requestId'),
      '生成了却没传等于没生成'
    )
  })

  test('后端业务 code 全部登记进前端 i18n 白名单', () => {
    const codes: Array<[string, string]> = [
      ['qy_rel_self_invite', 'relation'],
      ['qy_rel_user_not_found', 'relation'],
      ['qy_rel_already_bound', 'relation'],
      ['qy_rel_cycle', 'relation'],
      ['qy_rel_not_bound', 'relation'],
      ['qy_rel_conflict', 'relation'],
      ['qy_adj_over_reclaimable', 'adjust'],
      ['qy_adj_overflow', 'adjust'],
      ['qy_adj_user_not_found', 'adjust'],
      ['qy_idem_key_conflict', 'adjust'],
    ]
    for (const [code, where] of codes) {
      const source = where === 'relation' ? relationBackend : adjustBackend
      assert.ok(
        source.includes(`"${code}"`),
        `后端不再返回 ${code}，前端映射成了死代码`
      )
      assert.ok(
        qyApi.includes(`${code}:`),
        `${code} 没登记进 QY_ERROR_CODE_I18N，用户会看到后端的中文原文`
      )
    }
  })

  test('解绑语义的两侧说法一致 —— 历史佣金保留、不再产生新的', () => {
    // 这不是文案洁癖：这句话是这个动作**唯一**的语义说明。后端审计正文与
    // 前端弹窗说了两件不同的事，等于没人知道解绑到底会不会把钱收走。
    assert.ok(
      relationBackend.includes('已产生的佣金全部保留'),
      '后端审计正文不再声明解绑语义，事后就没有任何地方能回答"那笔钱去哪了"'
    )
    assert.ok(
      zhKeys['qy_rel_unbind_semantics'].includes('已经产生的佣金全部保留'),
      '弹窗文案必须把"历史佣金保留"直接说出来'
    )
    assert.ok(
      zhKeys['qy_rel_unbind_semantics'].includes('冲正'),
      '文案必须指出"要收回已发放的佣金请走冲正"，否则运营会以为解绑就能收回'
    )
  })

  test('手工调整走账目行这件事必须写进文案', () => {
    assert.ok(
      adjustBackend.includes('SourceType: SourceManual'),
      '后端不再落 manual 计佣行了？那就是在直接改余额列'
    )
    assert.ok(
      zhKeys['qy_adj_ledger_note'].includes('计佣行'),
      '弹窗必须说明这一笔会落成一条可追溯的账目行，而不是把余额数字改掉'
    )
  })
})

describe('i18n 键完整', () => {
  const used = [
    'qy_rel_title',
    'qy_rel_authority',
    'qy_rel_authority_hint',
    'qy_rel_scope',
    'qy_rel_sort',
    'qy_rel_username_ph',
    'qy_rel_inviter_id_ph',
    'qy_rel_col_inviter',
    'qy_rel_col_invitee',
    'qy_rel_col_bound_at',
    'qy_rel_col_commission',
    'qy_rel_col_accruals',
    'qy_rel_bound_at_inferred',
    'qy_rel_bound_at_unknown',
    'qy_rel_state_bound',
    'qy_rel_state_unbound',
    'qy_rel_state_blocked',
    'qy_rel_user_gone',
    'qy_rel_history_only',
    'qy_rel_bind',
    'qy_rel_unbind',
    'qy_rel_pair',
    'qy_rel_empty_title',
    'qy_rel_empty_desc',
    'qy_rel_bind_title',
    'qy_rel_bind_desc',
    'qy_rel_bind_warning',
    'qy_rel_invitee_id',
    'qy_rel_invitee_id_hint',
    'qy_rel_inviter_id',
    'qy_rel_inviter_id_hint',
    'qy_rel_reason',
    'qy_rel_reason_hint',
    'qy_rel_bind_reason_ph',
    'qy_rel_bind_submit',
    'qy_rel_bind_ok',
    'qy_rel_unbind_title',
    'qy_rel_unbind_semantics',
    'qy_rel_kept_commission',
    'qy_rel_accrual_count',
    'qy_rel_unbind_reason_ph',
    'qy_rel_unbind_submit',
    'qy_rel_unbind_ok',
    'qy_err_rel_self_invite',
    'qy_err_rel_user_not_found',
    'qy_err_rel_already_bound',
    'qy_err_rel_cycle',
    'qy_err_rel_not_bound',
    'qy_err_rel_conflict',
    'qy_adj_action',
    'qy_adj_title',
    'qy_adj_ledger_note',
    'qy_adj_unsettled',
    'qy_adj_direction',
    'qy_adj_dir_add',
    'qy_adj_dir_sub',
    'qy_adj_amount',
    'qy_adj_amount_hint_add',
    'qy_adj_amount_hint_sub',
    'qy_adj_over_ceiling',
    'qy_adj_reason',
    'qy_adj_reason_ph',
    'qy_adj_reason_hint',
    'qy_adj_submit',
    'qy_adj_ok',
    'qy_adj_replayed',
    'qy_err_adj_over_reclaimable',
    'qy_err_adj_overflow',
    'qy_err_adj_user_not_found',
    'qy_err_adj_idem_conflict',
    // 手工调整这一路的来源标签,佣金审核页的筛选下拉要用。
    'qy_aff_src_manual',
    // 下面两组 key 是模板字符串拼出来的，静态扫不到，这里显式列全。
    ...QY_RELATION_SCOPES.map((scope) => `qy_rel_scope_${scope}`),
    ...QY_RELATION_SORTS.map((sort) => `qy_rel_sort_${sort}`),
  ]

  test('本页用到的每个键在 en 与 zh 里都有', () => {
    for (const key of used) {
      assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
      assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
    }
  })

  test('带插值的文案两侧占位符一致', () => {
    // 占位符对不上时 i18next 不会报错，只会原样留下 `{{ceiling}}` ——
    // 而那几句恰好是在告诉运营"最多能扣多少""保留了多少"。
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
})
