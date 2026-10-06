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
import { dirname, join, relative } from 'node:path'
import { test } from 'node:test'
import { fileURLToPath } from 'node:url'

import { newQyRequestId } from '../request-id'

// 「结算台 → 佣金用户」打开即报 crypto.randomUUID is not a function。
//
// crypto.randomUUID 只在安全上下文(HTTPS / localhost)里存在,旧浏览器也没有。
// 那一页挂着「调整佣金」弹窗,弹窗挂载时的 useEffect 就生成幂等键 —— 不用点任何
// 按钮,页面一打开就崩。幂等键与本地 id 一律走 newQyRequestId(nanoid,与环境无关)。

test('generates ids from the backend idempotency-key alphabet', () => {
  const id = newQyRequestId()
  assert.match(id, /^[A-Za-z0-9_-]+$/)
  assert.notEqual(newQyRequestId(), id)
})

test('no source file calls crypto.randomUUID directly', () => {
  const srcDir = join(dirname(fileURLToPath(import.meta.url)), '../../../../..')
  // 先探测再调用的那一处是合法的(有回退);其余任何直接调用都会在 HTTP 下崩。
  const allowed = new Set(['lib/auth-session-sync.ts'])
  const offenders: string[] = []
  const walk = (dir: string) => {
    for (const name of readdirSync(dir)) {
      const full = join(dir, name)
      if (statSync(full).isDirectory()) {
        if (name !== '__tests__' && name !== 'node_modules') walk(full)
        continue
      }
      if (!/\.tsx?$/.test(name)) continue
      const rel = relative(srcDir, full).replaceAll('\\', '/')
      if (allowed.has(rel)) continue
      const code = readFileSync(full, 'utf8').replaceAll(
        /\/\*[\s\S]*?\*\/|\/\/.*$/gm,
        ''
      )
      if (/crypto\.randomUUID\s*\(/.test(code)) offenders.push(rel)
    }
  }
  walk(srcDir)
  assert.deepEqual(offenders, [], '改用 newQyRequestId(),它不依赖安全上下文')
})
