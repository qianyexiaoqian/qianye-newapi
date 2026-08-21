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
 * 「立即结算」在前端**一个入口都没有**，而「重跑今天这一轮」一个都没少。
 *
 * # 为什么这两件事必须一起断言
 *
 * 项目方原话：「佣金审核的这个：立即结算 移除吧，全部由系统到时间自动结算。」
 * 这两条动作长得像、名字里都有 settle、后端路径还共前缀，所以最可能的坏法是
 * **连坐删错**：
 *
 *   POST /admin/commission/settle        按人补一笔，绕过下一轮
 *   POST /admin/commission/settle/rerun  把今天那一行运行记录改回"还要再跑"
 *
 * 后者是当天重试次数烧完之后**整轮补救的唯一入口**（本仓刚修过"重试次数烧完
 * 当天再也不会自动跑"）。删掉它的界面等于把一次故障拖成两次。所以正向断言
 * （立即结算为零）与反向断言（重跑还在，而且还接着一个按钮）必须同屏。
 *
 * # 后端接口为什么留着
 *
 * 移除的是**界面入口**，不是能力。`POST /api/qy/admin/commission/settle` 原样
 * 保留，它是"某一个邀请人卡住"时的兜底；`qianye/modules/commission/
 * settle_rerun_boundary_test.go` 已经在断言这条路由仍然挂在管理端组上（重跑
 * 维持 ADMIN 的理由建立在"它比这几条动钱的动作更轻"之上），所以这里不重复。
 *
 * # 撤掉按钮的代价必须被补上
 *
 * 运营点不到按钮、又不知道什么时候到账，只会来问人。所以这里还要钉住：
 * 佣金审核那一屏上**写着**自动结算的时点，并且**指得出**手动补救在哪里。
 */
import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import type { TFunction } from 'i18next'

import type { NavGroup, NavItem } from '@/components/layout/types'
import type { QyFeatures } from '@/features/qy/lib/types'
import { mergeQyNavGroups } from '@/features/qy/nav'
import en from '@/i18n/qy/en.json'
import zh from '@/i18n/qy/zh.json'
import { ROLE } from '@/lib/roles'

// __tests__ → admin-settlement → pages → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..'
)

const enKeys = en as Record<string, string>
const zhKeys = zh as Record<string, string>

/** `src` 下所有 ts/tsx 源码（不含测试文件本身：它必然写着被禁的字面量）。 */
function sourceFiles(): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const full = join(dir, name)
      if (statSync(full).isDirectory()) {
        if (name === 'node_modules' || name === '__tests__') continue
        walk(full)
        continue
      }
      if (!/\.tsx?$/.test(name)) continue
      if (/\.test\.tsx?$/.test(name)) continue
      out.push(full)
    }
  }
  walk(srcDir)
  return out
}

const sources = sourceFiles().map((file) => ({
  path: relative(srcDir, file).replaceAll('\\', '/'),
  text: readFileSync(file, 'utf8'),
}))

describe('「立即结算」已从前端移除', () => {
  test('扫描器确实扫到了源码（否则下面全是空转）', () => {
    assert.ok(
      sources.length > 500,
      `只扫到 ${sources.length} 个源文件，扫描器多半坏了`
    )
    assert.ok(
      sources.some(
        (file) => file.path === 'features/qy/pages/admin-commission/api.ts'
      ),
      '扫描器漏了佣金那组接口封装 —— 它正是"立即结算"曾经住的地方'
    )
  })

  test('没有任何源文件再调 POST /admin/commission/settle', () => {
    // 判据是**引号紧贴着的路径字面量**，不是"文中出现过这几个字"：
    //   · 注释里解释"这条接口原样保留、只摘了界面"必须仍然写得出来；
    //   · rerun 共前缀，`(?![/\w])` 把 `/admin/commission/settle/rerun`
    //     排除在外 —— 连坐删错是这条断言最容易犯的错。
    const call = /['"`]\/admin\/commission\/settle(?![/\w])/
    const offenders = sources
      .filter((file) => call.test(file.text))
      .map((file) => file.path)
    assert.deepEqual(
      offenders,
      [],
      `以下文件仍然在调"立即结算"：\n${offenders.join('\n')}`
    )
  })

  test('它的两条文案也一起删了（留着就是被翻译工具带着走的死键）', () => {
    for (const key of ['qy_cm_settle', 'qy_cm_settle_ok']) {
      assert.equal(zhKeys[key], undefined, `zh.json 里还留着 ${key}`)
      assert.equal(enKeys[key], undefined, `en.json 里还留着 ${key}`)
    }
  })

  test('「重跑今天这一轮」一个都没少 —— 它不是同一件事', () => {
    const api = sources.find(
      (file) => file.path === 'features/qy/pages/admin-commission/api.ts'
    )
    assert.ok(api != null)
    assert.ok(
      api.text.includes("'/admin/commission/settle/rerun'"),
      '重跑的封装被连坐删了：当天重试次数烧完之后，整轮补救就只剩 curl'
    )

    const page = sources.find(
      (file) => file.path === 'features/qy/pages/admin-commission/index.tsx'
    )
    assert.ok(page != null)
    for (const marker of ['qyRerunDailySettle', 'qy_cm_ds_rerun']) {
      assert.ok(
        page.text.includes(marker),
        `结算调度卡上的「重跑」按钮没了（缺 ${marker}）：接口还在，但界面上按不到`
      )
    }
  })
})

describe('撤掉按钮之后，"什么时候自动结算"必须写在界面上', () => {
  const body = sources.find(
    (file) =>
      file.path === 'features/qy/pages/admin-commission-records/index.tsx'
  )

  test('佣金审核那一屏上写着自动结算的时点', () => {
    assert.ok(body != null)
    for (const key of ['qy_cm_auto_settle', 'qy_cm_auto_settle_plain']) {
      assert.ok(
        body.text.includes(key),
        `佣金审核上没有 ${key}：运营点不到按钮，也没有任何地方告诉他什么时候到账`
      )
    }
  })

  test('那段文案里的三个数都是后端下发的，前端不自己算', () => {
    assert.ok(body != null)
    for (const field of [
      'day_offset_minutes',
      'next_run_after',
      'payout_day_offset',
    ]) {
      assert.ok(
        body.text.includes(field),
        `${field} 没有被用上：日界 / 下一轮开跑 / T+N 少一个，这段话就答不全`
      )
    }
    // T+N 里那个 +1（桶要等一整天结束才封板）绝不能在前端重算：
    // 后端 payoutDayOffset、用户端 policy 已经是两处，第三处必然漂移。
    assert.ok(
      !body.text.includes('holding_days'),
      '前端拿 holding_days 自己算 T+N 了：holding_days=0 也是 T+1，这个 +1 已经错过一次'
    )
  })

  test('文案里指得出手动补救在哪一页', () => {
    assert.ok(body != null)
    assert.ok(
      body.text.includes('qy_cm_auto_settle_fallback'),
      '没有告诉运维手动补救还在：摘掉界面等于把它变成只能用 curl 的隐藏功能'
    )
    assert.ok(
      body.text.includes("to='/qy/admin/commission'"),
      '那句话没有链接到结算调度所在的页面'
    )
    assert.ok(
      body.text.includes("hash='qy-daily-settle'"),
      '链接没有带锚点：佣金设置页很长，落到顶部等于让人自己找'
    )

    const settings = sources.find(
      (file) => file.path === 'features/qy/pages/admin-commission/index.tsx'
    )
    assert.ok(settings != null)
    assert.ok(
      settings.text.includes("id='qy-daily-settle'"),
      '锚点在目标页上不存在：链接点过去只会停在页面顶部'
    )
  })

  test('三条文案在 en 与 zh 里都有', () => {
    for (const key of [
      'qy_cm_auto_settle',
      'qy_cm_auto_settle_plain',
      'qy_cm_auto_settle_fallback',
    ]) {
      assert.ok(zhKeys[key] != null, `zh.json 缺少 ${key}`)
      assert.ok(enKeys[key] != null, `en.json 缺少 ${key}`)
    }
  })
})

/*
 * 三合一之后侧栏上**只剩一行**，而且没有指向已收进选择夹那三页的孤儿项。
 *
 * `nav-merge.test.ts` 冻结了整组的内容，这里断的是本次改动最直接的那个后果：
 * 三行变一行。两条不是重复——那边一旦有人"顺手"把某一页加回去，冻结列表也会
 * 被一起改；这里的判据不依赖那份快照，而是"这三个 url 一个都不许出现在侧栏"。
 */
describe('结算三页在侧栏上收成一行', () => {
  const t = ((key: string) => key) as unknown as TFunction
  const ALL_ON: QyFeatures = {
    transfer: true,
    commission: true,
    withdraw: true,
    availability: true,
    lottery: true,
    violation: true,
    ticket: true,
    group_matrix: true,
    pay_password: true,
  }

  function sidebarUrls(): string[] {
    const urls: string[] = []
    const walk = (items: readonly NavItem[]) => {
      for (const item of items) {
        if (typeof item.url === 'string') urls.push(item.url)
        const children = (item as { items?: readonly NavItem[] }).items
        if (children != null) walk(children)
      }
    }
    const base: NavGroup[] = [
      { id: 'general', title: 'General', items: [] },
      { id: 'personal', title: 'Personal', items: [] },
      { id: 'admin', title: 'Admin', items: [] },
    ]
    for (const group of mergeQyNavGroups(base, ALL_ON, ROLE.SUPER_ADMIN, t)) {
      walk(group.items)
    }
    return urls
  }

  test('宿主恰好一行，三张标签一行都没有', () => {
    const urls = sidebarUrls()
    assert.equal(
      urls.filter((url) => url === '/qy/admin/settlement').length,
      1,
      '「结算台」在侧栏上不是恰好一行'
    )
    for (const url of [
      '/qy/admin/daily-consume',
      '/qy/admin/commission-records',
      '/qy/admin/withdrawals',
    ]) {
      assert.ok(
        !urls.includes(url),
        `${url} 还留在侧栏上：点进去会被重定向甩到宿主页，运营会以为自己点错了`
      )
    }
  })
})
