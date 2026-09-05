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

import {
  QY_NAV_GROUPS,
  QY_PAGES,
  QY_SETTINGS_GROUP,
  QY_SETTINGS_PAGES,
  QY_TAB_GROUPS,
  isQyAdminPage,
  isQyPageHosted,
  isQyPageVisible,
  qyEntryPages,
  qyTabHash,
  qyTabTarget,
} from '@/features/qy/lib/pages'
import type { QyFeatures } from '@/features/qy/lib/types'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'

/**
 * `lib/pages.ts` 是页面清单的唯一登记处（标题 / 功能开关 / 侧栏落点 / 图标 /
 * 页面代号合成一行）。合并之前这些信息散在三个文件里，本项目反复踩
 * "同一概念的第 N 份拷贝各自漂移"，所以这里把结构不变式全部钉死。
 */

// __tests__ → lib → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..'
)
const sidebarDataSource = readFileSync(
  join(srcDir, 'hooks', 'use-sidebar-data.ts'),
  'utf8'
)

const enKeys: Record<string, string> = en as Record<string, string>
const zhKeys: Record<string, string> = zh as Record<string, string>

/**
 * 并进上游「系统设置」抽屉的 6 页（需求 8）。
 *
 * 写成快照而不是从 `QY_SETTINGS_PAGES` 反推：反推等于用被测数据证明被测数据，
 * 把「佣金审核」误标成配置页也照样全绿。界线是**配置 vs 流水**：
 * 改了会影响后续每一笔的进抽屉，审核/记录留在根侧栏。
 */
const SETTINGS_URLS = [
  // 佣金配置（D-15 恢复）：费率 / 分组档 / 持有期 / 自动入账参数。改一次影响之后
  // 每一笔计佣与入账，与抽屉里那批配置同类；星屑侧的邀请返比例仍在星屑配置页。
  '/qy/admin/commission',
  '/qy/admin/transfer-config',
  '/qy/admin/transfer-group-rules',
  // `/qy/admin/group-matrix` 已不在本表：分组矩阵整体搬进了上游抽屉的
  // 「计费与支付 → 用户分组」section（见
  // `features/system-settings/billing/section-registry.tsx`），旧 url 只保留
  // 重定向，因此它不再是 qy 自己那一组折叠菜单的成员。
  //
  // `/qy/admin/user-group` 同理已不在本表，但它连重定向都没留：那一页整页只有
  // 「新注册用户落哪个分组」一个下拉，已经降级成同一个 section 上的一张卡片
  // （`features/qy/pages/admin-user-groups/default-group`）。
  '/qy/admin/violation-rules',
  // 违规类型：它与「违规判定规则」是同一件事的两层（类型定「几次会被处置」，
  // 规则定「什么算一次」），改一次影响之后每一笔,与抽屉里那批配置同类。
  '/qy/admin/violation-categories',
  // AI 内容审核:审核渠道、密钥、抽样率、成本。改一次影响之后每一笔请求
  // (要不要送审、送到哪、花多少钱),与抽屉里那批配置同类。
  // 它不是流水页 —— 页面上的成本统计是这份配置的**后果**,不是每天要过的账。
  '/qy/admin/violation-ai-review',
  '/qy/admin/lottery-config',
  '/qy/admin/api-address',
  // 受限账号。项目方原话：「受限制账号，在系统设置里面单独进行配置。」
  // 它进抽屉的判据与同组其余几页一致：页面上唯一可写的东西（受限账号公告）
  // 改一次影响之后**每一个**被限制的账号的首屏；另外两块（计数、可达面清单）
  // 是只读现状，不是每天要过的流水。
  '/qy/admin/restricted-accounts',
  // 星屑配置（design-15）：货币名、消费返 / 邀请返费率、分组覆盖、套餐返。
  // 改一次影响之后每一笔结算，与抽奖配置同一档。
  '/qy/admin/stardust-config',
]

/** 明确**留在根侧栏**的管理页。它们进了抽屉就是运营每天多点两下。 */
const ROOT_ADMIN_URLS = [
  // 「邀请管理」：邀请关系 / 下线日消费 / 日结明细三张标签的宿主（D-14 之前是
  // 「结算台」与「用户佣金」两行）。三张表都是运营每天要过的流水 / 台账，
  // 不是"改一次影响后续每一笔"的配置。
  '/qy/admin/invite',
  // 日消费明细是运营每天要开的报表（"昨天谁消费了多少"），不是"改一次影响
  // 后续每一笔"的配置，所以留根侧栏。
  '/qy/admin/daily-consume',
  '/qy/admin/invite-accruals',
  // 「结算台」（D-15 恢复）：日消费明细 / 佣金审核 / 佣金用户三张标签的宿主与
  // 两个成员。全是每天要过的流水与台账。
  '/qy/admin/settlement',
  '/qy/admin/commission-records',
  '/qy/admin/commission-users',
  '/qy/admin/transfer-records',
  '/qy/admin/fund-orders',
  '/qy/admin/violations',
  '/qy/admin/audit-logs',
  '/qy/admin/health',
  '/qy/admin/lottery',
  // 工单审核台是每天要开的流水页，不是"改一次影响后续每一笔"的配置。
  '/qy/admin/tickets',
  // 星屑账本（余额 / 流水 / 日桶 / 手调）与商城管理（商品 / 订单 / 码库存）
  // 都是每天要开的台账，留根侧栏（新组「星屑运营」）。
  '/qy/admin/stardust',
  '/qy/admin/mall',
]

/**
 * 收进选择夹、因而**没有独立侧栏入口**的页面（需求 2 / 3，以及本轮的佣金收敛）。
 *
 * 写成快照而不是从 `QY_TAB_GROUPS` 反推：反推等于用被测数据证明被测数据，
 * 把三页搬错宿主也照样全绿。
 */
const HOSTED_URLS = [
  // 「邀请管理」的后两张标签（D-14）：下线日消费 / 日结明细。旧的
  // daily-consume 地址保留成重定向。
  '/qy/admin/daily-consume',
  '/qy/admin/invite-accruals',
  // 「结算台」的后两张标签（D-15）：佣金审核 / 佣金用户。第一张是宿主自己。
  '/qy/admin/commission-records',
  '/qy/admin/commission-users',
  '/qy/transfer',
  '/qy/transfer-logs',
  '/qy/pay-password',
  // 「我的推广」的后三张标签：下线 / 佣金明细（D-15）/ 返星屑明细。提现两张
  // 随提现模块永久删除，不回来。
  '/qy/invitees',
  '/qy/commission-records',
  '/qy/invite-records',
  // 需求 2（抽奖轮）：竞猜与我的参与收进 `/qy/lottery` 的选择夹，侧栏只剩一行。
  // 本轮追加双色球（项目方原话：「把双色球和竞猜分开选择夹，抽奖-竞猜-双色球」）：
  // 它同样是选择夹成员，侧栏仍然只有一行。
  '/qy/lottery-guess',
  '/qy/lottery-ball',
  '/qy/lottery-records',
  // 转盘（项目方 2026-09-05：「星屑转盘的页面移动到抽奖竞猜里面去」）：
  // 它此前在「娱乐」组独占一行，现在是同一个选择夹的第四张标签，侧栏仍然只有
  // 一行；旧地址保留成重定向。
  '/qy/wheel',
  // 星屑（design-15）：流水与待结算收进 `/qy/stardust`，我的订单收进 `/qy/mall`。
  // 三张表各自回答同一件事的一个切面，拆成三行侧栏入口只会让人在三处各找一遍。
  '/qy/stardust-ledger',
  '/qy/stardust-accruals',
  '/qy/mall-orders',
]

describe('qy page table structure', () => {
  test('恰好一个落点：有独立入口的写 group，被收进选择夹的不写', () => {
    for (const page of QY_PAGES) {
      if (isQyPageHosted(page.url)) {
        assert.equal(
          page.group,
          undefined,
          `${page.url} 已被收进选择夹，却还声明了 group=${page.group}；侧栏会多出一行点了就被重定向甩走的入口`
        )
        continue
      }
      assert.ok(
        page.group != null,
        `${page.url} 没有落点：它既不在选择夹里，也不属于任何分组，等于整个前端到不了`
      )
      // 二级落点只在抽屉里有意义：根侧栏上没有「模型与路由」这一组，
      // 标在别处就是一行永远不生效的死数据。
      assert.ok(
        page.settingsSection == null || page.group === QY_SETTINGS_GROUP,
        `${page.url} 不在系统设置抽屉里，settingsSection=${page.settingsSection} 不会生效`
      )
    }
  })

  test('根侧栏的一级项都有图标，抽屉子项与选择夹成员都没有', () => {
    for (const page of QY_PAGES) {
      if (isQyPageHosted(page.url)) {
        assert.equal(
          page.icon,
          undefined,
          `${page.url} 已经没有侧栏入口了，图标是死数据`
        )
      } else if (page.group === QY_SETTINGS_GROUP) {
        // 上游系统设置抽屉里那 7 组的子项都不带图标，混着给会让缩进看起来是坏的。
        assert.equal(
          page.icon,
          undefined,
          `${page.url} 在系统设置抽屉里，上游那一层的子项不带图标`
        )
        assert.equal(
          page.after,
          undefined,
          `${page.url} 不在根侧栏上，after 锚点是死数据`
        )
      } else {
        // 上游每个一级侧栏项都有 lucide 图标；qy 项漏图标会整行左对齐错位。
        assert.ok(page.icon != null, `${page.url} 是一级项，必须有图标`)
      }
    }
  })

  test('新增分组的成员在角色维度上是齐的', () => {
    for (const group of QY_NAV_GROUPS) {
      const members = QY_PAGES.filter((page) => page.group === group.id)
      assert.ok(members.length > 0, `分组 ${group.id} 没有任何页面`)
      const admin = members.filter((page) => isQyAdminPage(page.url))
      assert.ok(
        admin.length === 0 || admin.length === members.length,
        `分组 ${group.id} 混了 ${admin.length} 个管理页和 ${members.length - admin.length} 个用户页；分组的可见性跟着成员走，混编会让普通用户看到一个半空的「${group.titleKey}」分组，标题与实际内容对不上`
      )
    }
  })

  test('新增分组的规模不超过上游 admin 组（7 行）', () => {
    for (const group of QY_NAV_GROUPS) {
      const rows = QY_PAGES.filter((page) => page.group === group.id).length
      assert.ok(
        rows <= 7,
        `分组 ${group.id} 有 ${rows} 行，又变成一长条平铺了 —— 请拆分组或收进折叠项`
      )
    }
  })

  test('所有 after 锚点都是上游根侧栏里真实存在的 url', () => {
    const anchors = QY_PAGES.map((page) => page.after).filter(
      (anchor): anchor is string => anchor != null
    )

    assert.ok(anchors.length > 0, '锚点全丢了：qy 项会整体堆到各组末尾')
    for (const anchor of anchors) {
      assert.ok(
        sidebarDataSource.includes(`url: '${anchor}',`),
        `锚点 ${anchor} 在 hooks/use-sidebar-data.ts 里已经不存在（上游改名？）—— 合并会 fail-open 追加到组尾，位置不再是设计意图`
      )
    }
  })
})

describe('qy 选择夹（需求 2 / 3）', () => {
  test('七个选择夹的成员逐项冻结', () => {
    assert.deepEqual(
      QY_TAB_GROUPS.map((group) => [group.host, [...group.pages]]),
      [
        ['/wallet', ['/qy/transfer', '/qy/transfer-logs', '/qy/pay-password']],
        [
          // 「我的推广」（D-15）：概览 / 下线 / 佣金明细 / 返星屑明细。提现两张标签
          // 随提现模块整体删除、不回来；佣金明细是佣金账本的逐笔与自动入账记录。
          '/qy/affiliate',
          [
            '/qy/affiliate',
            '/qy/invitees',
            '/qy/commission-records',
            '/qy/invite-records',
          ],
        ],
        [
          // 项目方原话：「把双色球和竞猜分开选择夹，抽奖-竞猜-双色球。」
          // 顺序逐字照抄那句话；2026-09-05 追加「星屑转盘的页面移动到抽奖竞猜
          // 里面去」，转盘接在双色球之后（它仍是一种玩法），「我的参与」压在
          // 最后 —— 它不是一种玩法，是查票与领奖的地方。把双色球排到竞猜前面
          // （"两种抽奖挨着"看起来更整齐）就与项目方点名的顺序不符了。
          '/qy/lottery',
          [
            '/qy/lottery',
            '/qy/lottery-guess',
            '/qy/lottery-ball',
            '/qy/wheel',
            '/qy/lottery-records',
          ],
        ],
        [
          // 「邀请管理」（D-14）。顺序 = 运营对账时的追问顺序：谁邀请了谁 →
          // 下线昨天花了多少 → 按邀请人分组档该返多少。它取代了「结算台」与
          // 「用户佣金」两个选择夹。
          '/qy/admin/invite',
          [
            '/qy/admin/invite',
            '/qy/admin/daily-consume',
            '/qy/admin/invite-accruals',
          ],
        ],
        [
          // 「结算台」（D-15 恢复）：日消费明细（宿主自己）/ 佣金审核 / 佣金用户。
          // 顺序 = 钱流动的顺序：谁花了多少 → 记了多少 → 进星屑多少。
          '/qy/admin/settlement',
          [
            '/qy/admin/settlement',
            '/qy/admin/commission-records',
            '/qy/admin/commission-users',
          ],
        ],
        [
          // 星屑：余额 / 流水 / 待结算，宿主 = 第一张（与 /qy/affiliate 同形）。
          '/qy/stardust',
          ['/qy/stardust', '/qy/stardust-ledger', '/qy/stardust-accruals'],
        ],
        [
          // 商城：商品 / 我的订单。
          '/qy/mall',
          ['/qy/mall', '/qy/mall-orders'],
        ],
      ],
      '选择夹的成员或顺序变了：项目方点名要的是「发起划转/划转记录/支付密码」、D-15 的「概览/下线/佣金明细/返星屑明细」与「日消费明细/佣金审核/佣金用户」、D-14 的「邀请关系/下线日消费/日结明细」、「抽奖/竞猜/双色球/星屑转盘/我的参与」，以及 design-15 的「余额/流水/待结算」与「商品/我的订单」'
    )
  })

  /**
   * 邀请管理的入口数**恰好一**（D-14）。
   *
   * 此前侧栏「结算」组里佣金相关有两行（结算台 / 用户佣金），更早是三行。
   * 佣金账本删除之后只剩「邀请管理」一个宿主；这条断言盯的是反向漂移：
   * 有人为了"方便"把日消费明细或日结明细再放回侧栏，第二行就回来了。
   */
  test('侧栏上的邀请入口恰好一个：邀请管理宿主', () => {
    const rows = QY_PAGES.filter(
      (page) =>
        (page.url.startsWith('/qy/admin/invite') ||
          page.url === '/qy/admin/daily-consume') &&
        !isQyPageHosted(page.url)
    ).map((page) => page.url)
    assert.deepEqual(rows, ['/qy/admin/invite'])
  })

  /**
   * 标签数**固定为五**：四张玩法（抽奖 / 竞猜 / 双色球 / 星屑转盘）+ 我的参与。
   *
   * 四变五的唯一理由是项目方 2026-09-05 的原话：「星屑转盘的页面移动到抽奖
   * 竞猜里面去。」—— 转盘此前在侧栏独占一行，现在是这个选择夹的一张标签。
   * 这一条反向盯的仍然是"再来一种玩法就再加一张标签"：`draw_mode` 日后再
   * 长出新的定档方式时，它要并进这四张夹之一（`hallLanes` 是玩法的一个划分），
   * 而不是占一个新的导航位 —— 标签数随后台配置浮动的话，用户每次进来看到的
   * 标签栏都不一样，而侧栏那一行的语义也就没法固定。
   *
   * 断言的是**数字**而不是"等于 QY_TAB_GROUPS 里那一行的长度"：后者等于用
   * 被测数据证明被测数据，加一张标签也照样绿。
   */
  test('抽奖选择夹恰好五张标签', () => {
    const group = QY_TAB_GROUPS.find((item) => item.host === '/qy/lottery')
    assert.equal(group?.pages.length, 5)
  })

  test('被收进选择夹的正好是这 17 页', () => {
    assert.deepEqual(
      QY_PAGES.filter((page) => isQyPageHosted(page.url))
        .map((page) => page.url)
        .sort(),
      [...HOSTED_URLS].sort()
    )
  })

  test('宿主页自己不算"被收进去"，因此仍有侧栏入口', () => {
    for (const group of QY_TAB_GROUPS) {
      assert.equal(
        isQyPageHosted(group.host),
        false,
        `${group.host} 被判成了选择夹成员，它自己的侧栏入口会消失`
      )
    }
    const affiliate = QY_PAGES.find((page) => page.url === '/qy/affiliate')
    assert.ok(affiliate?.group != null, '我的推广的宿主页丢了侧栏落点')
  })

  test('每个标签都在页面表里登记过（否则宿主页会渲染一张没有标题的空标签）', () => {
    const known = new Set(QY_PAGES.map((page) => page.url))
    for (const group of QY_TAB_GROUPS) {
      for (const url of group.pages) {
        assert.ok(
          known.has(url),
          `${group.host} 的标签 ${url} 不在 QY_PAGES 里`
        )
      }
    }
  })

  /**
   * 宿主页的可见性**跟着标签走**：标签全关时宿主整行消失，不留一行点进去
   * 空白的入口。
   *
   * D-14 之前这条还用「只开提现、不开返佣」的结算台守过反方向（宿主自己的开关
   * 关掉、某张标签还开着 → 宿主仍然可见）；提现删除后每个 qy 宿主的标签都挂
   * 同一个功能开关，那条分支只剩展示开关一种异构 —— 由下面「只开转盘」那条
   * 覆盖，两条合起来才把 `isQyPageVisible` 里"任一标签可见即可见"钉死。
   */
  test('三张标签全关时「我的推广」整行消失', () => {
    const inviteOff: QyFeatures = {
      transfer: true,
      invite: false,
      commission: false,
      availability: true,
      lottery: true,
      violation: true,
      ticket: true,
      group_matrix: true,
      pay_password: true,
      stardust: true,
      mall: true,
    }
    const host = QY_PAGES.find((page) => page.url === '/qy/affiliate')
    assert.ok(host != null)
    assert.equal(isQyPageVisible(host, inviteOff, false), false)
    assert.ok(
      !qyEntryPages(inviteOff, false).some(
        (page) => page.url === '/qy/affiliate'
      ),
      '三张标签全关了，「我的推广」还留在侧栏上：点进去是一片空白'
    )
    // 开关打开时宿主与它的两张标签一起回来（标签本身不占侧栏一行）。
    const on = { ...inviteOff, invite: true }
    const urls = qyEntryPages(on, false).map((page) => page.url)
    assert.ok(urls.includes('/qy/affiliate'))
    assert.ok(!urls.includes('/qy/invitees'))
    assert.ok(!urls.includes('/qy/commission-records'))
    assert.ok(!urls.includes('/qy/invite-records'))
  })

  test('qyEntryPages 把选择夹成员滤掉（侧栏与工作区索引页共用这一处判定）', () => {
    const all: QyFeatures = {
      transfer: true,
      invite: true,
      commission: true,
      availability: true,
      lottery: true,
      violation: true,
      ticket: true,
      group_matrix: true,
      pay_password: true,
      stardust: true,
      mall: true,
    }
    const urls = qyEntryPages(all, true).map((page) => page.url)
    for (const url of HOSTED_URLS) {
      assert.ok(
        !urls.includes(url),
        `${url} 仍被当成独立入口：侧栏与 /qy 索引页都会给它留一行死链`
      )
    }
    // 宿主页与没被收进去的页面必须还在，否则就是一刀切掉太多。
    assert.ok(urls.includes('/qy/affiliate'))
    assert.ok(urls.includes('/qy/availability'))
    assert.equal(urls.length, QY_PAGES.length - HOSTED_URLS.length)
  })

  /**
   * 需求原文：「系统设置前端是否显示」。
   *
   * 两条断言缺一不可：只钉"用户侧会消失"，把开关误接到管理页上也照样全绿，
   * 而那样一来关掉之后就再也没有地方能把它重新打开 —— 这个仓库反复出现的
   * 「写了但没接」的另一种形状。
   */
  test('展示开关关掉时用户侧入口消失，管理端入口不受影响', () => {
    const all: QyFeatures = {
      transfer: true,
      invite: true,
      commission: true,
      availability: true,
      lottery: true,
      violation: true,
      ticket: true,
      group_matrix: true,
      pay_password: true,
      stardust: true,
      mall: true,
    }
    const off = qyEntryPages(all, true, {
      lottery: false,
      wheel: false,
      stardust: false,
      mall: false,
    }).map((page) => page.url)
    assert.ok(!off.includes('/qy/lottery'), '用户侧大厅仍留在导航里')
    assert.ok(!off.includes('/qy/lottery-records'), '我的记录仍留在导航里')
    assert.ok(
      off.includes('/qy/admin/lottery'),
      '管理端入口被一起藏掉了：关掉之后就再也没有地方能把它打开'
    )
    // 星屑 / 商城两个开关同一形状：用户侧消失、管理端不受影响。
    // （转盘不在这里：它已是抽奖竞猜选择夹的一张标签，本来就不是独立入口，
    // 写在这条清单里是恒真的空转。它的开关由下面那条"宿主跟着标签走"覆盖。）
    for (const url of ['/qy/stardust', '/qy/mall']) {
      assert.ok(!off.includes(url), `${url} 的展示开关关掉后仍留在导航里`)
    }
    for (const url of ['/qy/admin/stardust', '/qy/admin/mall']) {
      assert.ok(off.includes(url), `${url} 被用户侧的展示开关一起藏掉了`)
    }

    // 不传展示开关时一律按"显示"处理：配置还在取数的那一帧不该把菜单先抹掉。
    const unknown = qyEntryPages(all, true).map((page) => page.url)
    assert.ok(unknown.includes('/qy/lottery'))
  })

  /**
   * 转盘并入选择夹之后，宿主那一行的可见性要跟着转盘标签走（与「结算台」
   * 跟着提现审核走是同一条规则）。
   *
   * 反向盯的是"把 `wheel` 一格从 QyEntrySwitches 里删掉、只留 `lottery`"：
   * 那样"只开转盘"的站点会因为 `lottery` 为假而整行消失，转盘明明开着却
   * 没有任何入口 —— 正是并入之前那条注释警告过的形状，换了个方向再出现。
   */
  test('只开转盘时抽奖竞猜那一行仍然可见；转盘也关掉时才消失', () => {
    const all: QyFeatures = {
      transfer: true,
      invite: true,
      commission: true,
      availability: true,
      lottery: true,
      violation: true,
      ticket: true,
      group_matrix: true,
      pay_password: true,
      stardust: true,
      mall: true,
    }
    const wheelOnly = qyEntryPages(all, true, {
      lottery: false,
      wheel: true,
      stardust: true,
      mall: true,
    }).map((page) => page.url)
    assert.ok(
      wheelOnly.includes('/qy/lottery'),
      '只开转盘时抽奖竞猜那一行消失了 —— 转盘标签开着却没有入口'
    )
    const none = qyEntryPages(all, true, {
      lottery: false,
      wheel: false,
      stardust: true,
      mall: true,
    }).map((page) => page.url)
    assert.ok(
      !none.includes('/qy/lottery'),
      '五种玩法全关时抽奖竞猜那一行还在：点进去只剩「我的参与」'
    )
  })

  test('qyTabTarget 直接落到宿主页 + 对应标签，而不是先跳旧路由再被弹回来', () => {
    assert.deepEqual(qyTabTarget('/qy/transfer-logs'), {
      to: '/wallet',
      hash: 'qy-transfer-logs',
    })
    assert.deepEqual(qyTabTarget('/qy/invite-records'), {
      to: '/qy/affiliate',
      hash: 'qy-invite-records',
    })
    // 宿主页自己也是组里的一张标签，所以它也会被指到自己 + hash。
    assert.deepEqual(qyTabTarget('/qy/affiliate'), {
      to: '/qy/affiliate',
      hash: 'qy-affiliate',
    })
    // 不在任何选择夹里的页面原样返回，调用方不需要分支。
    assert.deepEqual(qyTabTarget('/qy/violations'), { to: '/qy/violations' })
  })

  test('hash 片段互不相同且与 url 一一对应', () => {
    const hashes = QY_TAB_GROUPS.flatMap((group) =>
      group.pages.map((url) => qyTabHash(url))
    )
    assert.equal(
      new Set(hashes).size,
      hashes.length,
      '两张标签算出了同一个 hash'
    )
    assert.equal(qyTabHash('/qy/transfer-logs'), 'qy-transfer-logs')
    assert.equal(qyTabHash('/wallet'), 'wallet')
  })
})

describe('qy page table routes', () => {
  /**
   * 每个登记的 url 都必须有一个真实的路由文件。
   *
   * 这条守卫此前不存在，代价是 `/qy/lottery-guess` 登记进了 `QY_PAGES` 与
   * GATE 编号表、却没有对应的路由 —— 站内不会产生死链（选择夹成员会被
   * `qyEntryPages` 从侧栏滤掉，站内跳转一律走 `qyTabTarget`），所以任何测试
   * 都不会红；只有从工单/聊天里拿到手敲地址的用户会撞上 404，而 GATE
   * 给它分配的编号永远渲染不出来。
   *
   * 判据是"文件存在"而不是"routeTree 里有"：routeTree.gen.ts 是构建产物，
   * 拿它当判据会让这条守卫依赖某次构建有没有跑过。
   */
  test('每个登记的 url 都有对应的路由文件', () => {
    const routesDir = join(srcDir, 'routes', '_authenticated')
    const missing = QY_PAGES.map((page) => page.url).filter((url) => {
      const rel = url.replace(/^\//, '')
      return (
        !existsSync(join(routesDir, `${rel}.tsx`)) &&
        !existsSync(join(routesDir, rel, 'index.tsx'))
      )
    })
    assert.deepEqual(
      missing,
      [],
      `以下 url 登记在 QY_PAGES 里但没有路由文件，手敲地址会 404：\n${missing.join('\n')}`
    )
  })
})

describe('qy page table i18n', () => {
  test('每个登记的 key 在 en 与 zh 里都存在', () => {
    const keys = [
      ...QY_PAGES.flatMap((page) => [page.titleKey, page.codeKey]),
      ...QY_NAV_GROUPS.map((group) => group.titleKey),
      ...QY_TAB_GROUPS.map((group) => group.titleKey),
      'qy_nav_group_settings',
    ]
    for (const key of keys) {
      assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
      assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
    }
  })

  test('en 与 zh 的键数相等', () => {
    assert.equal(
      Object.keys(enKeys).length,
      Object.keys(zhKeys).length,
      'qy 的 en/zh 必须逐键对齐'
    )
  })

  test('并进系统设置抽屉的成员逐项冻结（需求 8）', () => {
    assert.deepEqual(
      QY_SETTINGS_PAGES.map((page) => page.url).sort(),
      [...SETTINGS_URLS].sort(),
      '进抽屉的页面换人了：界线是"配置进抽屉、审核与流水留在根侧栏"'
    )
    // 反方向：流水/审核页一个都不许溜进去。只钉正向的话，把「佣金审核」
    // 也标上 QY_SETTINGS_GROUP、同时改一下上面那张快照，就能一路全绿。
    const settings = new Set(QY_SETTINGS_PAGES.map((page) => page.url))
    for (const url of ROOT_ADMIN_URLS) {
      assert.ok(
        !settings.has(url),
        `${url} 是运营每天要开的流水/审核页，埋进设置抽屉里更难找`
      )
    }
    // 两张快照合起来必须盖满所有管理页，漏登记一页会静默逃过上面两条。
    assert.deepEqual(
      QY_PAGES.filter((page) => isQyAdminPage(page.url))
        .map((page) => page.url)
        .sort(),
      [...SETTINGS_URLS, ...ROOT_ADMIN_URLS].sort(),
      '新增了管理页却没在本测试里表态它该进抽屉还是留根侧栏'
    )
  })
})

/**
 * 需求 6：侧栏侧栏英文副标已整体去掉。
 *
 * 这三条是**反向**断言，专门盯"改了又长回来"：数据（`enKey`）、文案
 * （`qy_sg_nav_en_*`）、渲染（`nav-group.tsx` 的挂载点）三处任何一处复活都会红。
 * 只钉其中一处不够 —— 本仓反复出现的形状正是"数据还在但没人渲染"（死数据）
 * 与"组件渲染了但数据没了"（空节点）。
 */
describe('侧栏侧栏英文副标已移除（需求 6）', () => {
  test('页面表里不再有 enKey 这类装饰性副标字段', () => {
    for (const page of QY_PAGES) {
      assert.equal(
        (page as Record<string, unknown>).enKey,
        undefined,
        `${page.url} 又挂上了 enKey：侧栏副标只有 qy 项有、上游项没有，正是被投诉的不齐`
      )
    }
  })

  test('i18n 里不再有 qy_sg_nav_en_* 文案', () => {
    const leftovers = [
      ...Object.keys(enKeys).filter((key) => key.startsWith('qy_sg_nav_en')),
      ...Object.keys(zhKeys).filter((key) => key.startsWith('qy_sg_nav_en')),
    ]
    assert.deepEqual(
      leftovers,
      [],
      '侧栏英文副标文案是死键，会一直被翻译工具带着走'
    )
  })

  test('nav-group.tsx 与上游一致：没有任何 qy 的挂载点', () => {
    const navGroupSource = readFileSync(
      join(srcDir, 'components', 'layout', 'components', 'nav-group.tsx'),
      'utf8'
    )
    assert.ok(
      !navGroupSource.includes('features/qy'),
      'nav-group.tsx 又 import 了 qy 的东西：上游侧栏渲染件应保持零改动'
    )
    assert.ok(
      !navGroupSource.includes('qy-sg-nav-en'),
      'nav-group.tsx 里又出现了侧栏英文副标节点'
    )
  })
})
