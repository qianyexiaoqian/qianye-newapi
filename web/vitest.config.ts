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
import { readdirSync, readFileSync, statSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { defineConfig } from 'vitest/config'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

/**
 * 本仓有两套前端测试运行器,`include` 必须把它们分开。
 *
 * 上游 rc.33 起用 vitest;本仓原有的一千多条用例写的是 `node:test` +
 * `node:assert`,由 `bun run test`(scripts/run-tests.mjs)跑。两边同名同后缀,
 * 靠 glob 分不开:vitest 捡到 node:test 文件会在 bundle 阶段直接失败
 * (Cannot bundle Node.js built-in "node:test"),200 份文件一起变红,
 * 真回归被淹掉。scripts/run-tests.mjs 那一侧做的是镜像判断。
 *
 * 判据同样是"文件里有没有 from 'vitest'",而不是维护一份会随上游漂移的名单。
 */
function collectVitestFiles(): string[] {
  const root = path.resolve(__dirname, 'src')
  const files: string[] = []
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const full = path.join(dir, name)
      if (statSync(full).isDirectory()) {
        if (name === 'node_modules') continue
        walk(full)
        continue
      }
      if (!/\.(test|spec)\.tsx?$/.test(name)) continue
      if (!/from ['"]vitest['"]/.test(readFileSync(full, 'utf8'))) continue
      files.push(path.relative(__dirname, full).split(path.sep).join('/'))
    }
  }
  walk(root)
  return files.sort()
}

export default defineConfig({
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test-setup.ts'],
    // Several heavy jsdom suites (channel-configuration, visual-billing-editor)
    // legitimately take >5s per test on contended CI runners; the vitest
    // default of 5000ms fails whichever of them crosses the line first. The
    // heaviest test measures ~3.2s uncontended, so 20s keeps headroom for the
    // ~4x slowdown observed on shared runners.
    testTimeout: 20000,
    clearMocks: true,
    restoreMocks: true,
    include: collectVitestFiles(),
    server: {
      deps: {
        // `@lobehub/icons` 传递依赖 `@emoji-mart/data` 的 JSON 资源。默认它被
        // 外部化,交给 Node 的 ESM loader 去 import,而 Node 22+ 要求 JSON 导入
        // 带 `with { type: 'json' }` —— 于是任何走到品牌图标的用例整份加载失败
        // (报 "needs an import attribute of type json",一条用例都跑不到)。
        // 内联之后由 Vite 转译,JSON 正常解析。
        inline: [
          '@lobehub/icons',
          '@lobehub/ui',
          '@emoji-mart/data',
          /@lobehub\//,
          /antd-style/,
        ],
      },
    },
  },
})
