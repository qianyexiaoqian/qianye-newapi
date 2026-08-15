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

/**
 * 「管理员重置支付密码」的接线断言。
 *
 * 这个功能的后端在本轮之前就**已经全部写好并注册好了**（`handleAdminReset` +
 * `POST /pay-password/:user_id/reset`，审计、限流、事由必填一应俱全），
 * 缺的只有前端入口 —— 也就是本仓累计出现十几次的头号缺陷形状的完成态：
 * **写对了，但没接上**，而全仓测试一路全绿。
 *
 * 因此这里守的不是渲染细节，而是三条只会在运行时才现形的链路：
 *
 *  1. 【断链】弹窗组件存在，但用户管理的行操作菜单里没有任何地方渲染它 ——
 *     管理员永远点不到，而 typecheck 与其余单测都不会有信号；
 *  2. 【路径漂移】前端拼的 URL 与后端真实注册的路由对不上（少一段、参数名
 *     写错、忘了 `/admin` 前缀）。表现是一个 404，只有真的点下去才知道；
 *  3. 【必填项丢失】后端对重置**强制要求事由**（`errReasonRequired`），
 *     前端不发 `reason` 就是一个必然失败的按钮。
 *
 * 判据一律取源码文本而不是渲染结果：这三件事都发生在"组件有没有被用上"这一层，
 * 挂载一棵 React 树反而测不到（不渲染那一行菜单项的组件同样能挂载成功）。
 */

// __tests__ → components → qy → features → src → web → 仓库根
const repoRoot = join(
  dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
  '..',
  '..'
)
const webSrc = join(repoRoot, 'web', 'src')

const read = (...parts: string[]) => readFileSync(join(...parts), 'utf8')

const rowActions = read(
  webSrc,
  'features',
  'users',
  'components',
  'data-table-row-actions.tsx'
)
const payPasswordApi = read(webSrc, 'features', 'qy', 'lib', 'pay-password.ts')
const routeManifest = read(repoRoot, 'qianye', 'route_manifest.txt')

describe('管理员重置支付密码的接线', () => {
  // 判据是 `<QyResetPayPasswordDialog`（**带尖括号**的 JSX 用法）而不是裸的
  // 组件名。回滚验证当场抓到后者是假回归：只删掉那段 JSX、留下 import，
  // 裸名字断言照样绿 —— 而那正是"弹窗还在，但谁也点不开"的形态。
  test('用户管理的行操作菜单真的渲染了重置弹窗', () => {
    assert.ok(
      rowActions.includes('<QyResetPayPasswordDialog'),
      '用户管理的行操作里没有渲染 <QyResetPayPasswordDialog>：' +
        '弹窗写好了却没有任何入口能打开它，管理员永远点不到'
    )
    assert.ok(
      rowActions.includes("t('qy_pp_admin_reset_menu')"),
      '行操作菜单里没有「重置支付密码」这一项：' +
        '弹窗即使被渲染，也没有任何东西会把它打开'
    )
  })

  test('入口跟着扩展开关走，扩展关掉时不渲染', () => {
    // 扩展关掉时后端整棵 /api/qy/** 都不注册，点进去只会拿到 404。
    assert.ok(
      rowActions.includes('useQyConfig()'),
      '入口没有读扩展配置：扩展未启用的站点会看到一个必然 404 的菜单项'
    )
  })

  /**
   * 两条断言缺一不可，理由是回滚验证抓出来的：
   *
   * 只比对"路径在 route_manifest.txt 里"是不够的 —— 把 `/reset` 改成
   * `/unlock` 时它照样绿，因为解锁**也是一条真实存在的路由**。而那次改动的
   * 后果是「重置支付密码」这个按钮实际上只解了锁：管理员以为用户的密码被清了，
   * 用户那边旧密码仍然有效，工单闭环在一个错误的前提上。
   * 所以必须先钉死"就是 reset 这一条"，再拿 manifest 交叉验证后端还留着它。
   */
  test('前端拼的重置 URL 就是后端的 reset 路由，不是隔壁的 unlock', () => {
    // qyPost 会补上 /api/qy 前缀；把模板参数还原成后端的路由参数名再比。
    const match = payPasswordApi.match(
      /qyAdminResetPayPassword[\S\s]*?qyPost<[^>]*>\(\s*`(\/admin\/pay-password\/[^`]+)`/
    )
    if (match === null) {
      assert.fail(
        'pay-password.ts 里找不到 qyAdminResetPayPassword 的 qyPost 调用（函数被删了，或路径不再是模板字符串）'
      )
    }
    const backendPath = `/api/qy${match[1].replace('${params.userId}', ':user_id')}`
    assert.equal(
      backendPath,
      '/api/qy/admin/pay-password/:user_id/reset',
      '重置函数打的不是 reset 路由。打到 unlock 上时"重置密码"实际只解了锁：管理员以为清空了，用户的旧密码还能用'
    )
    assert.ok(
      routeManifest.split('\n').includes(`POST ${backendPath}`),
      `POST ${backendPath} 不在 qianye/route_manifest.txt 里 —— 后端那条路由被改名或删了，点下去只会拿到 404`
    )
  })

  test('重置请求带上后端强制要求的事由', () => {
    assert.ok(
      /reason:\s*params\.reason/.test(payPasswordApi),
      '重置请求体里没有 reason：后端 errReasonRequired 会把每一次重置都拒掉'
    )
  })
})
