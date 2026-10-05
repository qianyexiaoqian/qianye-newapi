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
import { describe, expect, test } from 'vitest'

import {
  isModelSquareNavShown,
  parseHeaderNavModulesFromStatus,
} from '../nav-modules'

// 顶部导航与侧栏的「模型广场」入口共用这一条判据;侧栏那一项是为登录后的手机端加的,
// 它与顶部导航一旦口径不同,就会出现「设置里关了、手机侧栏还在」这类入口漂移。
type Case = {
  name: string
  status: Record<string, unknown> | null
  restricted: boolean
  expected: boolean
}

const withPricing = (pricing: unknown): Record<string, unknown> => ({
  HeaderNavModules: JSON.stringify({ pricing }),
})

describe('isModelSquareNavShown', () => {
  test.each<Case>([
    {
      name: 'shows the entry when the pricing module is enabled for a normal account',
      status: withPricing({ enabled: true, requireAuth: false }),
      restricted: false,
      expected: true,
    },
    {
      name: 'hides the entry when the pricing module is disabled in system settings',
      status: withPricing({ enabled: false, requireAuth: false }),
      restricted: false,
      expected: false,
    },
    {
      name: 'hides the entry for a restricted account even when pricing is enabled',
      status: withPricing({ enabled: true, requireAuth: false }),
      restricted: true,
      expected: false,
    },
    {
      name: 'falls back to the enabled default when status has not loaded yet',
      status: null,
      restricted: false,
      expected: true,
    },
  ])('$name', (c) => {
    const modules = parseHeaderNavModulesFromStatus(c.status)
    expect(isModelSquareNavShown(modules, c.restricted)).toBe(c.expected)
  })
})
