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
 * 受限账号这一段的**落点**判据。
 *
 * 项目方原话：「受限制账号，在系统设置里面单独进行配置。」上一轮它挨着站点公告
 * 住在「内容管理」里 —— 那不是"单独一段"，而是别人那一页上的一小节。
 *
 * 这个文件钉两件源码级的事，两件都不是渲染结果、`route-entry-guard` 也管不到：
 *
 *   ① 新段落**真的是一段**：进了系统设置抽屉，而且这一页上三块内容齐全
 *      （公告表单 / 受限账号计数 / 受限账号可达面）。少任何一块，这一页就退化回
 *      「一个公告表单换了个地方」，而项目方要的是把受限态的信息收在一屏上。
 *
 *   ② 原位置**不留孤儿表单**。这是搬家最贵的失败方式：内容管理里留一份能填、
 *      能点保存、却写到别处（或干脆没接上）的副本，运营在旧位置改完以为改了，
 *      线上一个字没变。本仓反复栽的「以为改了其实没改」正是这个形状。
 *      判据不是"文件里没有 form 字样"，而是**写接口的调用点全站恰好一处**。
 */
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, relative } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import type { TFunction } from 'i18next'

import { QY_SETTINGS_PAGES } from '@/features/qy/lib/pages'
import {
  CONTENT_SECTION_IDS,
  getContentSectionNavItems,
} from '@/features/system-settings/content/section-registry.tsx'

const t = ((key: string) => key) as unknown as TFunction

// __tests__ → admin-restricted-accounts → pages → qy → features → src
const srcDir = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..'
)

const PAGE_URL = '/qy/admin/restricted-accounts'
const MOVED_SECTION_ID = 'qy-restricted-notice'

/** 全站 `.ts`/`.tsx` 源码（不含测试），用于"某个调用点全站有几处"这类判据。 */
function sourceFiles(): string[] {
  const out: string[] = []
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir)) {
      const p = join(dir, entry)
      if (statSync(p).isDirectory()) {
        if (entry === '__tests__' || entry === 'node_modules') continue
        walk(p)
        continue
      }
      if (!/\.tsx?$/.test(entry) || entry.endsWith('.d.ts')) continue
      out.push(p)
    }
  }
  walk(srcDir)
  return out
}

describe('受限账号：新段落', () => {
  test('它是系统设置抽屉里独立的一项，而不是别人那一页上的一小节', () => {
    const page = QY_SETTINGS_PAGES.find((item) => item.url === PAGE_URL)
    assert.ok(
      page != null,
      `${PAGE_URL} 不在系统设置抽屉的成员里 —— 项目方要的是"在系统设置里面单独进行配置"`
    )
    assert.equal(page.titleKey, 'qy_nav_a_restricted_accounts')
    // 挂任何一个 YAML 功能开关都会造出"受限状态照常发生，而配置它的那一页
    // 连入口都没有"：受限态由上游用户管理页上的禁用动作产生，与
    // violation / ticket 两个模块开没开完全无关。
    assert.equal(
      page.feature,
      undefined,
      '受限账号页挂上了功能开关 —— 关掉那个模块之后，受限账号照常存在，而这一页会从菜单里消失'
    )
  })

  test('这一页上三块内容齐全：公告 / 计数 / 可达面', () => {
    const source = readFileSync(
      join(
        srcDir,
        'features',
        'qy',
        'pages',
        'admin-restricted-accounts',
        'index.tsx'
      ),
      'utf8'
    )
    for (const [marker, why] of [
      ['QyRestrictedNoticeCard', '公告表单没挂上来，这一页就是个空壳'],
      [
        'qy_ra_overview_title',
        '受限账号计数没了 —— 管理员在这一页答不出"现在有几个人被限制"',
      ],
      [
        'qy_ra_cap_title',
        '受限账号可达面清单没了 —— 管理员答不出"受限到底限了什么"',
      ],
      ["to='/users'", '"点进去看是谁"的链接没了，计数变成一个死数字'],
    ] as const) {
      assert.ok(source.includes(marker), why)
    }
  })
})

describe('受限账号：原位置不留孤儿', () => {
  test('内容管理里的那份编辑面已经删除', () => {
    assert.ok(
      !existsSync(
        join(
          srcDir,
          'features',
          'system-settings',
          'content',
          'qy-restricted-notice-section.tsx'
        )
      ),
      '旧的编辑面文件还在 —— 两份表单读写同一份配置，迟早出现"在旧位置改完以为改了"'
    )
  })

  test('公告的写接口全站只有一个调用点，且在新家里', () => {
    // 判据取"写接口的调用点"而不是"文件里有没有 <Input>"：一个只读的预览、
    // 一个被注释掉的表单都不是孤儿，而一个藏在别处、看起来无害却真的会 PUT
    // 的按钮才是。全站计数是唯一能把后者揪出来的判据。
    const callers = sourceFiles().filter((file) =>
      // 负向后视排掉**定义**那一处（`lib/restricted-notice.ts` 里的
      // `export function putQyRestrictedNotice(`）：传输层声明一个写函数是对的，
      // 有问题的是"有几个界面在按它"。
      /(?<!function )putQyRestrictedNotice\s*\(/.test(
        readFileSync(file, 'utf8')
      )
    )
    assert.deepEqual(
      callers.map((file) => relative(srcDir, file).replaceAll('\\', '/')),
      [
        'features/qy/pages/admin-restricted-accounts/components/notice-card.tsx',
      ],
      '受限账号公告的保存动作出现在不止一处（或搬错了地方）'
    )
  })

  test('留在内容管理里的那一段没有任何输入控件', () => {
    const source = readFileSync(
      join(
        srcDir,
        'features',
        'system-settings',
        'content',
        'qy-restricted-notice-moved.tsx'
      ),
      'utf8'
    )
    for (const control of ['<Input', '<Textarea', '<Switch', 'useMutation']) {
      assert.ok(
        !source.includes(control),
        `路牌里出现了 ${control} —— 它必须只是一句话加一个链接，不能是第二份表单`
      )
    }
    // 路牌必须真的指向新家，否则它只是一段"搬走了"而不说搬去哪的告示。
    assert.ok(source.includes(PAGE_URL), `路牌没有指向 ${PAGE_URL}`)
  })

  test('旧深链接仍然落得到路牌，但菜单里不再有这一项', () => {
    // section id 保留 = `/system-settings/content/qy-restricted-notice` 这条
    // 深链接（书签、工单里贴出去的地址）落到路牌上。删掉 id 的话，`$section`
    // 路由会把人**静默重定向**回「数据看板」，既没有报错也没有解释。
    assert.ok(
      (CONTENT_SECTION_IDS as readonly string[]).includes(MOVED_SECTION_ID),
      '旧 section id 被删了 —— 旧深链接会被静默重定向回默认段，管理员只会以为自己记错了地址'
    )

    const navUrls = getContentSectionNavItems(t).map((item) => item.url)
    assert.ok(
      !navUrls.some((url) => url.endsWith(`/${MOVED_SECTION_ID}`)),
      '菜单里还留着「受限账号公告」—— 一个点进去只告诉你去别处的常驻菜单项是纯噪声'
    )
    // 只滤掉这一项，别的 section 一个都不许跟着消失。上一版这条过滤写在共享的
    // registry 工具里时，很容易连累另外 6 个设置页。
    assert.equal(
      navUrls.length,
      CONTENT_SECTION_IDS.length - 1,
      '内容管理的菜单项数量对不上：除了「受限账号公告」这一项，别的 section 不该被滤掉'
    )
  })
})
