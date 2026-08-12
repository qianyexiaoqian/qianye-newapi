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

describe('qy 前后端路径对账', () => {
  const routes = loadQyRouteManifest(REPO)
  const scan = collectQyRequestPaths(SRC)

  test('清单与调用点都不是空的', () => {
    // 这条守的是守卫自己：提取器一旦被改坏（扫不到文件、正则失配），
    // 下面每一条断言都会以「没有任何反例」的姿态变绿 —— 一个看起来最像
    // 「一切正常」的失效方式。
    assert.ok(
      routes.length > 150,
      `后端路由清单只有 ${routes.length} 条，太少了`
    )
    assert.ok(
      scan.sites.length > 150,
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
      literals.length > 150,
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
