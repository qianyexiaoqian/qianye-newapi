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

import {
  QY_COMMISSION_USER_FILTERS,
  QY_COMMISSION_USER_SORTS,
  QY_RELATION_ACTIONS,
} from '../types'

/**
 * 「用户佣金」页的接线断言。
 *
 * 渲染逻辑由 `user-commission-flow.test.tsx` 用真 DOM 守；这里守的是四条
 * **只会在运行时才现形**的链路：
 *
 *  1. 断链：本页是整个佣金管理的按人入口，路由 + 页面表 + 选择夹三处任何一处
 *     缺失，站内就再也没有"按用户看佣金"这件事 —— 而所有单测照样全绿。
 *     本仓已经五次栽在这个形状上；
 *  2. 同一概念的第 N 份拷贝：DTO 字段名、排序键、筛选键、四个关系动作对应的
 *     端点，在前后端各写一份。后端改了前端不会有任何反应，用户看到的是
 *     "整列空白"和"筛选选了没用"；
 *  3. 项目方今天撞到的那个按钮：手工调整产生的计佣行 `invitee_id = 0`，
 *     对着它点「拉黑」必然 400。治本是**根本不渲染**这个按钮；
 *  4. i18n 键缺失：`t()` 找不到键时原样吐出键名，屏幕上会出现
 *     `qy_cu_rel_semantics` 这种字符串 —— 而那一句正是"改绑定之后钱怎么办"。
 */

// __tests__ → admin-commission-users → pages → qy → features → src → web → 仓库根
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
const qyPages = join(webSrc, 'features', 'qy', 'pages')
const commissionDir = join(repoRoot, 'qianye', 'modules', 'commission')

const read = (...parts: string[]) => readFileSync(join(...parts), 'utf8')

const usersBackend = read(commissionDir, 'api_admin_users.go')
const relationBackend = read(commissionDir, 'api_admin_relation.go')
const listPage = read(qyPages, 'admin-commission-users', 'index.tsx')
const hubPage = read(qyPages, 'admin-commission-users', 'hub.tsx')
const apiFile = read(qyPages, 'admin-commission-users', 'api.ts')
const typesFile = read(qyPages, 'admin-commission-users', 'types.ts')
const relationDialog = read(
  qyPages,
  'admin-commission-users',
  'components',
  'manage-relation-dialog.tsx'
)
const drilldown = read(
  qyPages,
  'admin-commission-users',
  'components',
  'user-commission-drilldown.tsx'
)
const recordsPage = read(qyPages, 'admin-commission-records', 'index.tsx')

const USERS_URL = '/qy/admin/commission-records/users'
const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

describe('用户佣金页的入口链路', () => {
  /**
   * 「写了但找不到」是本仓最贵的一类缺陷（五次）。通用守卫在
   * `lib/__tests__/route-entry-guard.test.ts`，这里从**本页这一侧**再钉一遍
   * 落点，因为那条通用守卫只知道"有入口"，不知道"入口在项目方要的位置上"。
   */
  test('路由文件挂的是宿主组件，路由 id 与目录一致', () => {
    const route = read(
      webSrc,
      'routes',
      '_authenticated',
      'qy',
      'admin',
      'commission-records',
      'users',
      'index.tsx'
    )
    assert.ok(
      route.includes('QyAdminCommissionUsersHub'),
      '路由没挂宿主组件，三张标签一张都渲染不出来'
    )
    assert.ok(
      route.includes("'/_authenticated/qy/admin/commission-records/users/'"),
      '路由 id 与目录结构不一致，生成的 routeTree 会指向别处'
    )
  })

  test('宿主页给齐了三张标签的正文', () => {
    for (const url of [
      USERS_URL,
      '/qy/admin/commission-records/relations',
      '/qy/admin/commission-records/balances',
    ]) {
      assert.ok(
        hubPage.includes(`'${url}'`),
        `宿主页的 bodies 里没有 ${url} —— QyPageTabs 会安静地跳过它，那一页就此从前端消失`
      )
    }
    // 宿主页不许自己再列一遍标签顺序（唯一真源是 QY_TAB_GROUPS）。
    assert.ok(!hubPage.includes('TabsTrigger'))
  })

  test('佣金审核页链得到本页（侧栏之外的第二条入口）', () => {
    assert.ok(
      recordsPage.includes(USERS_URL),
      '佣金审核页丢了指向用户佣金的按钮：运营正看着一笔计佣时，这是他跳去看"这个人整体什么情况"的那一跳'
    )
  })
})

describe('前后端口径一致', () => {
  /**
   * DTO 字段名逐个对上后端 `userCommissionView` 的 json tag。
   *
   * 对不上的表现是**整列空白**（`undefined` 渲染成空）而不是报错，所以它必须
   * 由机器盯着：本页有 20 多个字段，人眼核对一次就够了，核对第二次不会有人做。
   */
  test('列表 DTO 的字段名与后端 userCommissionView / balanceView 逐个对应', () => {
    // 后端 `userCommissionView` **内嵌** `balanceView`，下发的字段是两个结构体
    // 的并集；前端对应地把 `QyCommissionUser` 定义成 `QyCommissionBalance` 的
    // 超集。所以这里两边都扫，两个 TS 类型都认 —— 只扫其中一个的话，
    // "同一个数在两张表上叫两个名字"这种漂移会静默通过。
    const balancesTypes = read(qyPages, 'admin-commission-balances', 'types.ts')
    const blocks = [
      /type userCommissionView struct \{([\s\S]*?)\n\}/.exec(usersBackend),
      /type balanceView struct \{([\s\S]*?)\n\}/.exec(
        read(commissionDir, 'api_admin_balance.go')
      ),
    ]
    const backendFields: string[] = []
    for (const block of blocks) {
      assert.ok(block != null, '后端结构体声明找不到了，正则多半坏了')
      backendFields.push(
        ...[...block[1].matchAll(/json:"([a-z_]+)"/g)].map((m) => m[1])
      )
    }
    assert.ok(backendFields.length >= 25, '解析到的字段太少，正则多半坏了')
    for (const field of backendFields) {
      const declared = new RegExp(`^\\s{2}${field}\\??:`, 'm')
      assert.ok(
        declared.test(typesFile) || declared.test(balancesTypes),
        `后端下发 ${field}，而 QyCommissionUser（含它继承的 QyCommissionBalance）里没有这个字段 —— 那一列会渲染成空白，而不是报错`
      )
    }
  })

  test('排序键与后端 userCommissionSorters 逐个对应', () => {
    // 后端拿不到白名单里的键就静默回落默认排序：前端多一个键的表现是
    // "选了没反应"，少一个键的表现是"后端支持的排序用不到"。
    const block =
      /var userCommissionSorters = map\[string\]func\(a, b userCommissionView\) bool\{([\s\S]*?)\n\}/.exec(
        usersBackend
      )
    assert.ok(block != null, '后端 userCommissionSorters 声明找不到了')
    const backendSorts = [...block[1].matchAll(/^\t"([a-z_]+)":/gm)].map(
      (m) => m[1]
    )
    assert.deepEqual([...QY_COMMISSION_USER_SORTS].sort(), backendSorts.sort())
  })

  /**
   * 三个筛选开关的 query 参数名。
   *
   * 写错一个字母的表现是**筛选完全不生效**：后端读不到那个 query 就当没筛，
   * 于是列表照常返回全部行，运营会以为"这个站上所有人都有下线"。
   */
  test('筛选开关的参数名与后端分支逐字一致', () => {
    for (const flag of QY_COMMISSION_USER_FILTERS) {
      assert.ok(
        usersBackend.includes(`c.Query("${flag}") == "true"`),
        `后端不再按 ${flag} 分流，前端那个开关就是个摆设`
      )
    }
    // 反向：后端支持的筛选前端一个都不许漏，否则那条能力对用户不存在。
    const backendFlags = [
      ...usersBackend.matchAll(/c\.Query\("(\w+)"\) == "true"/g),
    ]
      .map((m) => m[1])
      .filter((flag, index, all) => all.indexOf(flag) === index)
    assert.deepEqual(
      [...QY_COMMISSION_USER_FILTERS].sort(),
      backendFlags.sort(),
      '后端多/少了一个筛选分支，前端 QY_COMMISSION_USER_FILTERS 没跟上'
    )
  })

  test('列表路由真的注册了，前端也真的打它', () => {
    assert.ok(
      usersBackend.includes('g.GET("/commission/users"'),
      '后端不再注册 /commission/users，前端这条查询会 404 → 被当成"扩展未启用"而静默隐藏'
    )
    assert.ok(
      apiFile.includes("'/admin/commission/users'"),
      '前端没有打 /admin/commission/users'
    )
    // 下钻的四张标签**一个新端点都不新增**，全部复用既有接口。多打一个新端点
    // 就是多一份要与后端同步演进的契约，而它们回答的问题既有接口已经能答。
    for (const reused of [
      'qyAdminAccrualsQuery',
      'qyAdminWithdrawalsQuery',
      'qyAdminRelationsQuery',
    ]) {
      assert.ok(
        drilldown.includes(reused),
        `下钻不再复用 ${reused}：它多半自己新开了一个端点`
      )
    }
  })

  /**
   * 四个关系动作各自打哪条路由。
   *
   * 尤其是**换绑**：它必须走后端的原子 `rebind`，不能是前端"先解绑再绑"两步。
   * 两步的失败模式是第二步挂掉时这个人停在"没有上线"的中间态，而运营看到的
   * 只有一句"操作失败"，他会以为什么都没变。
   */
  test('换绑走后端的原子 rebind，不是前端两步拼的', () => {
    assert.ok(
      relationBackend.includes('g.POST("/commission/relations/rebind"'),
      '后端不再提供原子换绑，前端只能退回两步拼接 —— 那会留下"没有上线"的中间态'
    )
    assert.ok(
      apiFile.includes("'/admin/commission/relations/rebind'"),
      '前端没有调用 rebind'
    )
    assert.ok(
      relationDialog.includes('qyRebindAffRelation('),
      '换绑弹窗没调 rebind：它多半又拼回了 unbind + bind 两步'
    )
    // 反向：弹窗里不许出现"先 unbind 再 bind"的痕迹。
    assert.ok(
      !/qyUnbindAffRelation\([\s\S]{0,400}qyBindAffRelation\(/.test(
        relationDialog
      ),
      '弹窗里出现了 unbind 紧接着 bind 的顺序调用 —— 那正是被 rebind 取代掉的非原子两步'
    )
  })

  test('四个关系动作都在弹窗里接了线', () => {
    for (const action of QY_RELATION_ACTIONS) {
      assert.ok(
        relationDialog.includes(`'${action}'`),
        `弹窗里没有 ${action} 这一档，下拉里选得到但点了什么都不会发生`
      )
    }
  })

  test('本页不自己写第二份资金逻辑', () => {
    // 手工增减复用余额页那一个弹窗（幂等键、扣减上限、审计全在后端那一份）。
    assert.ok(
      listPage.includes('AdjustCommissionDialog'),
      '本页没挂手工增减弹窗，项目方要的「编辑用户的佣金」就没有入口'
    )
    for (const forbidden of ['client_request_id', 'crypto.randomUUID']) {
      assert.ok(
        !listPage.includes(forbidden),
        `本页自己生成了 ${forbidden}：手工增减的幂等键必须只有一处实现`
      )
    }
  })

  test('金额一律走 QyAmountText，不显示裸 quota 整数', () => {
    // 裸整数会让运营拿它去和用户在钱包页看到的余额对话，两处口径不同就会
    // 得出"系统少算了他的钱"这种结论。
    for (const column of [
      'available_quota',
      'frozen_quota',
      'withdrawn_quota',
      'total_earned_quota',
      'total_clawback_quota',
    ]) {
      assert.ok(
        listPage.includes(`<QyAmountText quota={row.${column}} />`),
        `${column} 没走 QyAmountText`
      )
    }
  })
})

describe('拉黑按钮：没有邀请关系的行不许渲染它', () => {
  /**
   * 项目方今天撞到的那个缺陷。
   *
   * 手工调整产生的计佣行 `invitee_id = 0`（那笔钱不是从谁的消费里分出来的，
   * 不挂在任何下线上），对着它点「拉黑」后端必然 400。治本是**根本不渲染**，
   * 而不是把错误提示写得好看一点。
   */
  test('佣金审核页按 invitee_id > 0 条件渲染', () => {
    assert.ok(
      /\{row\.invitee_id > 0 \?/.test(recordsPage),
      '「拉黑」又变回无条件渲染：手工调整那些行上会重新出现一个点了必报错的按钮'
    )
    // 光藏起来不够：那一格必须说明"这一行为什么没有这个动作"，否则运营会
    // 以为页面坏了，转而去别处找同一个按钮。
    assert.ok(
      recordsPage.includes("t('qy_cm_block_na')"),
      '按钮藏了但没留下任何说明，那一格是一片空白'
    )
  })

  test('后端把「没有邀请关系」与「报文格式错」分成了两个 code', () => {
    // 前端的条件渲染是治本，这条 400 是第二道闸。两者缺一：只有条件渲染的话，
    // 直接打接口/并发改数据仍然会撞上；只有 code 的话，那个按钮还在。
    assert.ok(
      relationBackend.includes('"qy_rel_no_relation"') ||
        read(commissionDir, 'api_admin.go').includes('"qy_rel_no_relation"'),
      '后端又把这两种失败混回了同一个 qy_invalid_param'
    )
    const qyApi = read(webSrc, 'features', 'qy', 'lib', 'api.ts')
    assert.ok(
      qyApi.includes('qy_rel_no_relation:'),
      'qy_rel_no_relation 没登记进 QY_ERROR_CODE_I18N，会回落成"请求参数有误"'
    )
  })

  /**
   * 「参数错误」与「网络异常，请求可能已经生效」不许同屏。
   *
   * 后一句是这套错误体系里语义最重的一句 —— 它的意思是"钱可能已经动了"。
   * 判据是**每个 onError 只发一条 toast**：两条 `toast.error` 并排出现在同一个
   * 分支里，就是项目方看到的那个屏幕。
   */
  test('每个失败分支只出一条提示', () => {
    for (const [name, source] of [
      ['佣金审核页', recordsPage],
      ['下钻浮层', drilldown],
      ['关系管理弹窗', relationDialog],
    ] as const) {
      const handlers = [...source.matchAll(/onError:[\s\S]{0,240}?\n\s{4}\}/g)]
      for (const handler of handlers) {
        const toasts = handler[0].match(/toast\.error\(/g) ?? []
        assert.ok(
          toasts.length <= 1,
          `${name} 的一个 onError 里发了 ${toasts.length} 条 toast —— 两句同屏正是项目方看到的那个屏幕`
        )
      }
    }
  })
})

describe('i18n 键完整', () => {
  const used = [
    'qy_nav_commission_users_hub',
    'qy_cu_title',
    'qy_cu_scope',
    'qy_cu_scope_hint',
    'qy_cu_keyword_ph',
    'qy_cu_col_inviter',
    'qy_cu_col_invitees',
    'qy_cu_no_inviter',
    'qy_cu_blocked_invitees',
    'qy_cu_totals',
    'qy_cu_empty_title',
    'qy_cu_empty_desc',
    'qy_cu_view',
    'qy_cu_manage_relation',
    'qy_cu_drill_title',
    'qy_cu_drill_empty',
    'qy_cu_tab_accruals',
    'qy_cu_tab_settled',
    'qy_cu_tab_withdrawals',
    'qy_cu_tab_invitees',
    'qy_cu_settled_hint',
    'qy_cu_rel_action',
    'qy_cu_rel_semantics',
    'qy_cu_rel_target_hint',
    'qy_cu_rel_reason_ph',
    'qy_cm_block_na',
    'qy_cm_block_no_relation',
    'qy_cm_unblock',
    'qy_err_rel_same_inviter',
    'qy_sg_code_a_commission_users',
    // 下面几组是模板字符串拼出来的，字面量扫描器看不见，这里显式列全。
    ...QY_COMMISSION_USER_SORTS.map((sort) => `qy_cu_sort_${sort}`),
    ...QY_COMMISSION_USER_FILTERS.map((flag) => `qy_cu_filter_${flag}`),
    ...QY_RELATION_ACTIONS.map((action) => `qy_cu_rel_act_${action}`),
    ...QY_RELATION_ACTIONS.map((action) => `qy_cu_rel_desc_${action}`),
    ...QY_RELATION_ACTIONS.map((action) => `qy_cu_rel_ok_${action}`),
  ]

  test('本页用到的每个键在 en 与 zh 里都有', () => {
    for (const key of used) {
      assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
      assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
    }
  })

  /**
   * 「改绑定之后已经产生的佣金怎么办」——这句话是本页最重要的一句文案，
   * 项目方点名要求写进确认框。它必须**把答案说出来**，而不是一句"请谨慎操作"。
   */
  test('关系确认框把「历史保留、不再产生新的」直接说出来', () => {
    const zhText = zhKeys['qy_cu_rel_semantics']
    assert.ok(zhText.includes('保留'), '没说清历史佣金会被保留')
    assert.ok(
      zhText.includes('冲正'),
      '没指出"要收回已发放的佣金请走冲正"，运营会以为改绑定就能把钱收回来'
    )
    assert.ok(
      relationDialog.includes("t('qy_cu_rel_semantics')"),
      '这句话没有渲染在弹窗里'
    )
  })

  test('带插值的文案两侧占位符一致', () => {
    // 占位符对不上时 i18next 不报错，只会原样留下 `{{drift}}`。
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
