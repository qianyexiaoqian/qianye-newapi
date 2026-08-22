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
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, test } from 'node:test'
import { fileURLToPath } from 'node:url'

import {
  collectQyRequestPaths,
  loadQyRouteManifest,
  matchQyRoute,
} from '../../__tests__/qy-request-paths'

/**
 * route-contract.test.ts —— 前端调的每一条 qy 路径，后端必须真的注册了。
 *
 * ## 为什么这条测试非有不可
 *
 * 项目方为违规类型页报了**三次**「永久加载中」。真因是
 * `admin-violation-categories/api.ts` 里 6 处路径都少了 `/admin` 前缀
 * （后端挂在 `/api/qy/admin/violation/categories`），于是每一次请求都是 404。
 * 兄弟页 `admin-violation-rules/api.ts` 写的是带前缀的正确路径 —— 一个纯粹的
 * 抄漏，而三轮排查都没抓到它，原因是：
 *
 *   1. 前端测试**全部 mock 掉了 HTTP**，没有任何一条断言「这个路径在后端存在」；
 *   2. 排查用的本地 harness 把整段 `/api/qy/*` 挂载点替换掉了，错的路径也能拿到
 *      响应，反而把真缺陷盖住；
 *   3. 同期还有一条「请求永不落定」的缺陷压在上面，404 连浮出来的机会都没有。
 *
 * mock 是对的 —— 单元测试不该打网络。但 mock 之后就再没有任何一层在守
 * 「路径本身」，而路径恰恰是前后端之间唯一一个**纯字符串**的契约：它没有类型、
 * 没有编译期检查，改错一个字母到线上才会响。这条测试补的就是那一层。
 *
 * ## 两侧的数据从哪来
 *
 *   后端：`qianye/route_manifest.txt`，由 `qianye/route_manifest_test.go` 从
 *         **真实挂上的 gin 路由树**（`engine.Routes()`）导出。不是手抄的清单，
 *         也不是从 Go 源码正则抠的 —— 组前缀 `/api/qy`、`/api/qy/admin` 与参数
 *         段名都与线上逐字一致。清单过期时那条 Go 测试会红。
 *   前端：`features/qy/__tests__/qy-request-paths.ts`，扫源码里的调用点并求值出
 *         路径。提取逻辑**只有这一份**，审计与守卫共用 —— 写两份必然漂移，而
 *         那正是本轮缺陷的同款形状。
 */

const SRC = fileURLToPath(new URL('../../../../', import.meta.url))
const REPO = fileURLToPath(new URL('../../../../../../', import.meta.url))

/**
 * 求值不出路径的**显式**豁免清单。
 *
 * 每一条都必须写清「为什么这里没有可对账的路径」。不写理由的豁免等于把守卫关掉：
 * 下一个人会照着它再加一条，直到清单长过被守的东西。
 *
 * 清单是**按文件**的，且只对「路径求值不出来」这一档生效 —— 求得出来的路径就算
 * 在豁免文件里也照样对账。
 */
const UNRESOLVED_EXEMPT: { file: string; why: string }[] = [
  {
    file: 'features/qy/lib/api.ts',
    why:
      'qy 请求的传输层本身。`${QY_API_PREFIX}${path}` 里的 path 是形参，' +
      '这里没有任何具体路径可对账；真正的路径在各调用方，已被逐条扫到。',
  },
]

/**
 * 「路径求出来了，但后端清单里对不上」的显式豁免清单，同样必须写理由。
 *
 * 这一档刻意留得很短。参数段（`/withdraw/${id}/approve`）、可选查询串
 * （`${suffix}` 里的 `?reassign_to=…`）、以及三元分支（用户端/管理端两条图片
 * 路径）**都不需要**豁免：求值器会把求不出的 `${}` 同时展开成「一个路径参数」
 * 与「空串」两种候选，任一命中即算存在，它们本来就能对上。所以走到这一档的，
 * 要么是路径真的写错了，要么是像下面这样根本没有具体路径可谈。
 */
const MISS_EXEMPT: { file: string; line?: number; why: string }[] = [
  {
    file: 'features/qy/lib/api.ts',
    why:
      '同上，传输层自己。`api.get(`${QY_API_PREFIX}${path}`)` 里 QY_API_PREFIX 求得出、' +
      'path 求不出，于是它落在「求出来了但对不上」这一档而不是上面那一档 —— ' +
      '归一出来的 /api/qy 与 /api/:param 是两个不存在的路径，本来就不该有人注册。',
  },
]

/** Go 源码里的路由注册行 —— 只取字面量路径片段，用作清单过期的廉价兜底。 */
function goRouteLiterals(): { file: string; fragment: string }[] {
  const out: { file: string; fragment: string }[] = []
  const walk = (dir: string) => {
    for (const entry of readdirSync(dir)) {
      const p = join(dir, entry)
      if (statSync(p).isDirectory()) {
        walk(p)
        continue
      }
      if (!entry.endsWith('.go') || entry.endsWith('_test.go')) continue
      const src = readFileSync(p, 'utf-8')
      const re = /\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Any)\(\s*"([^"]*)"/g
      let m: RegExpExecArray | null
      while ((m = re.exec(src)) != null) {
        out.push({ file: p, fragment: m[2] })
      }
    }
  }
  walk(join(REPO, 'qianye'))
  return out
}

/**
 * 三个「不是空的」自检共用的下界。
 *
 * 它守的是**提取器自己被改坏**：三边（清单条数、前端调用点、Go 源码里的注册行）
 * 此刻实测都是 239，一一对应。原来的判据写成 `> 150`，与真实值差 88 条（37%）
 * —— 提取器可以静默丢掉三分之一的调用点（实测：walk 里跳过抽奖后台等五个目录
 * 就掉到 159）而这条仍然全绿，那正是它当初被建出来要防的形状。
 *
 * 留 9 条余量而不是钉死 239：删掉一个后台页面是正常改动，不该逼人改守卫；
 * 而任何一次「丢掉一整批」都远超这个余量。**真实条数明显涨上去之后要把这个
 * 下界一起抬上来**，否则它会慢慢退化回今天这个 150。
 */
/**
 * 「后端有、前端一处都不调」的显式豁免清单。
 *
 * 与 UNRESOLVED_EXEMPT 一样：每一条都要写清**为什么没有界面**。
 * 没有理由的孤儿就是忘了接线 —— 那正是本仓复发过五次以上的
 * 「实现了但界面上点不到」，而它不会让任何一条闸门变红。
 */
const ORPHAN_EXEMPT: { route: string; why: string }[] = [
  {
    route: 'GET /api/qy/admin/group-namespace/report',
    why:
      '分组命名空间的全量体检报告，一次输出几百行，是排障时 curl 的东西；' +
      '界面上真正要用的那几个数由 user-groups / model-groups / impact 三条接口给。',
  },
  {
    route: 'POST /api/qy/admin/group-namespace/backfill',
    why:
      '一次性回填历史分组登记，属于升级步骤而不是日常运营动作；' +
      '误点会在大表上跑一遍全量写入，刻意不给按钮。',
  },
  {
    route: 'PUT /api/qy/admin/group-namespace/user-groups/:name/default',
    why:
      '默认分组由「系统设置 → 计费与支付 → 用户分组」那张卡片改，' +
      '它走的是 PUT /api/qy/admin/user-group/config（同一件事的另一条口径）。' +
      '这一条是命名空间侧的等价入口，留给数据修复。',
  },
  {
    route: 'GET /api/qy/admin/group-ratio/orphans',
    why: 'admin-health 页的注释里已明说「想知道就去 curl」：它是对账输出，不是运营动作。',
  },
  {
    route: 'GET /api/qy/admin/log-metrics/health',
    why: '日志指标的探针端点，供外部监控轮询，不进管理界面。',
  },
  {
    route: 'POST /api/qy/admin/commission/cache/invalidate',
    why: '多节点缓存的手动收敛口，正常路径由版本号自动完成；留给排障。',
  },
  {
    route: 'POST /api/qy/admin/commission/settle',
    why:
      '「某一个邀请人卡住」的按人兜底。界面入口本轮**有意删除**（见结算台那一段）：' +
      '整轮补救走「结算调度 → 重跑今天这一轮」，按人兜底保留为 curl 通路。' +
      'qianye/modules/commission/settle_rerun_boundary_test.go 守着它仍挂在管理端组上。',
  },
  {
    route: 'GET /api/qy/lottery/series/:series_no',
    why:
      '双色球期次详情。前端的期次信息由活动详情一并下发（同一份 body 里就有 ' +
      'ball_result / issue_no），这条是给脚本客户端的等价读口。',
  },
  {
    route: 'GET /api/qy/ticket/images/:ref',
    why:
      '工单图片是 <img src> 直接指过去的，不经 axios —— 提取器只扫 axios 调用点，' +
      '所以它在这里必然是孤儿，而界面上一直看得见。',
  },
]

const MIN_COUNT = 230

describe('qy 前后端路径对账', () => {
  const routes = loadQyRouteManifest(REPO)
  const scan = collectQyRequestPaths(SRC)

  test('清单与调用点都不是空的', () => {
    // 这条守的是守卫自己：提取器一旦被改坏（扫不到文件、正则失配），
    // 下面每一条断言都会以「没有任何反例」的姿态变绿 —— 一个看起来最像
    // 「一切正常」的失效方式。
    assert.ok(
      routes.length >= MIN_COUNT,
      `后端路由清单只有 ${routes.length} 条，太少了`
    )
    assert.ok(
      scan.sites.length >= MIN_COUNT,
      `只扫到 ${scan.sites.length} 处 qy 调用点，提取器多半被改坏了`
    )
  })

  test('前端调的每一条路径，后端都注册了', () => {
    const misses: string[] = []
    for (const site of scan.sites) {
      if (matchQyRoute(site, routes) != null) continue
      if (
        MISS_EXEMPT.some(
          (e) =>
            e.file === site.file && (e.line == null || e.line === site.line)
        )
      ) {
        continue
      }
      misses.push(
        `${site.file}:${site.line}  ${site.method} ${site.expr}\n` +
          `      归一后：${site.candidates.join(' | ')}`
      )
    }
    assert.deepEqual(
      misses,
      [],
      `下面这些路径后端一条都没注册 —— 线上会是 404：\n    ${misses.join('\n    ')}\n` +
        '  （清单：qianye/route_manifest.txt，改过路由的话先重新生成）'
    )
  })

  /*
   * 反方向：后端注册了、前端一处都没调的路由。
   *
   * 正向对账(前端调的后端都注册了)防的是 404；反向防的是本仓自述复发过五次
   * 以上的**「实现了但界面上点不到」**——后端把接口写完、挂上、写了审计、
   * 写了 Go 测试，前端那一根线忘了接，于是它对运营来说等于不存在，
   * 而所有闸门都是绿的。
   *
   * 本轮实测:239 条后端路由里有 13 条没有任何前端调用点,其中四条是真的
   * 够不着的管理动作,最狠的一条(抽奖对账异常无法标记已解决)会让被报过异常的
   * 场次**永久删不掉**——删除闸门 checkActivityDeletable 的第五道就是它。
   *
   * 因此这里要求每一条孤儿路由都写进 ORPHAN_EXEMPT 并说明**为什么没有界面**。
   * 没有理由的孤儿 = 忘了接线,必须红。
   */
  test('后端注册了、前端一处都没调的路由，必须写清为什么没有界面', () => {
    const used = new Set<string>()
    for (const site of scan.sites) {
      const hit = matchQyRoute(site, routes)
      if (hit != null) used.add(`${hit.method} ${hit.path}`)
    }
    const orphans: string[] = []
    for (const route of routes) {
      const key = `${route.method} ${route.path}`
      if (used.has(key)) continue
      if (ORPHAN_EXEMPT.some((e) => e.route === key)) continue
      orphans.push(key)
    }
    assert.deepEqual(
      orphans,
      [],
      '下面这些后端路由没有任何前端调用点 —— 要么忘了接线（用户/运营点不到），' +
        '要么是有意只留给 curl。后者请写进 ORPHAN_EXEMPT 并说明理由：' +
        orphans
          .map(
            (line) => `
    ${line}`
          )
          .join('')
    )
  })

  /*
   * 反过来也要守：豁免清单里的路由必须真的还在后端注册着。
   * 接口删了而豁免留着，清单就会慢慢变成一堆没人看得懂的死条目。
   */
  test('孤儿豁免清单不许过期', () => {
    const all = new Set(routes.map((r) => `${r.method} ${r.path}`))
    const stale = ORPHAN_EXEMPT.filter((e) => !all.has(e.route)).map(
      (e) => e.route
    )
    assert.deepEqual(
      stale,
      [],
      '这些路由已经不在后端清单里了，豁免条目该一起删：' +
        stale
          .map(
            (line) => `
    ${line}`
          )
          .join('')
    )
  })

  /*
   * 第三个方向：豁免清单里的路由必须**真的还是孤儿**。
   *
   * 上面那两条都放行"接线接好了、豁免条目还留着"这种情况，而那份 `why` 此后
   * 就是一句假话。抽奖草稿编辑正是这么发生的：它的豁免理由写着「当下草稿改不了
   * 也删不了……不在本轮范围内」，等编辑表单真的补上之后，那段文字仍然会留在
   * 仓库里，被下一个人读成"这件事还没做"。
   *
   * 而豁免的全部价值就是那句 `why` 可信 —— 一份混着真孤儿与陈年假话的清单，
   * 比没有清单更糟。
   */
  test('豁免清单里的路由必须真的还是孤儿', () => {
    const used = new Set<string>()
    for (const site of scan.sites) {
      const hit = matchQyRoute(site, routes)
      if (hit != null) used.add(`${hit.method} ${hit.path}`)
    }
    const wired = ORPHAN_EXEMPT.filter((e) => used.has(e.route)).map(
      (e) => e.route
    )
    assert.deepEqual(
      wired,
      [],
      '这些路由已经有前端调用点了，豁免条目连同它那句「为什么没有界面」一起删：' +
        wired
          .map(
            (line) => `
    ${line}`
          )
          .join('')
    )
  })

  test('求不出路径的调用点，要么修好，要么写进豁免清单并说明理由', () => {
    const rogue = scan.unresolved.filter(
      (u) => !UNRESOLVED_EXEMPT.some((e) => e.file === u.file)
    )
    assert.deepEqual(
      rogue.map((u) => `${u.file}:${u.line}  ${u.expr}`),
      [],
      '这些调用点的路径求值不出来，守卫对它们是瞎的。' +
        '要么把路径写成可静态求值的形式，要么进 UNRESOLVED_EXEMPT 并写清理由。'
    )
    for (const exempt of UNRESOLVED_EXEMPT) {
      assert.ok(exempt.why.length > 20, `豁免 ${exempt.file} 没有写理由`)
    }
  })

  test('项目方报了三次的那一页，逐条钉住', () => {
    // 回归钉子。违规类型页的每一个动作都必须落在 /api/qy/admin/violation/categories
    // 之下 —— 少一个 /admin 前缀就是三次「永久加载中」的全部真因。
    const page = scan.sites.filter((s) =>
      s.file.includes('admin-violation-categories/api.ts')
    )
    assert.ok(page.length >= 6, `违规类型页只扫到 ${page.length} 处调用`)
    for (const site of page) {
      assert.ok(
        site.candidates.every((c) =>
          c.startsWith('/api/qy/admin/violation/categories')
        ),
        `${site.file}:${site.line} 的路径跑出了管理端组：${site.candidates.join(' | ')}`
      )
      assert.ok(matchQyRoute(site, routes) != null)
    }
  })

  test('路由清单没有过期（后端加了路由却没重新生成）', () => {
    // Go 侧 TestQyRouteManifestIsCurrent 才是权威守卫（它双向逐字比对）。
    // 这条是给「只跑前端测试」那趟 CI 的廉价兜底：新增路由而忘了重新生成清单时，
    // 上面那条对账会把新路径判成「后端没有」，报出来的却是前端的锅。
    const literals = goRouteLiterals()
    assert.ok(
      literals.length >= MIN_COUNT,
      `只在 Go 源码里找到 ${literals.length} 处路由注册`
    )
    const missing = literals.filter(
      ({ fragment }) =>
        fragment !== '' && !routes.some((r) => r.path.includes(fragment))
    )
    assert.deepEqual(
      missing.map((x) => `${x.file}  ${x.fragment}`),
      [],
      'Go 源码里注册了这些路径，清单里却没有 —— 重新生成：\n' +
        '    QY_ROUTE_MANIFEST_UPDATE=1 go test ./qianye/ -run TestQyRouteManifestIsCurrent -count=1'
    )
  })
})
