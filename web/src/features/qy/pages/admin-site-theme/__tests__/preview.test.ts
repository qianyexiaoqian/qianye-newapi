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
import { afterEach, describe, test } from 'node:test'

import {
  QY_PRESET_ATTRIBUTE,
  qyApplyPresetPreview,
  type QyPreviewTarget,
} from '../lib/preview'

type Op = { kind: 'remove' | 'set'; name: string; value?: string }

function recordingTarget(): { ops: Op[]; target: QyPreviewTarget } {
  const ops: Op[] = []
  return {
    ops,
    target: {
      setAttribute: (name: string, value: string) =>
        ops.push({ kind: 'set', name, value }),
      removeAttribute: (name: string) => ops.push({ kind: 'remove', name }),
    } as QyPreviewTarget,
  }
}

const realLocalStorage = Reflect.get(globalThis, 'localStorage')
const realDocument = Reflect.get(globalThis, 'document')

/** 任何一次持久化尝试都直接抛，让「预览开始落盘」这件事变成红色测试。 */
function forbidPersistence() {
  Reflect.set(globalThis, 'localStorage', {
    getItem: () => null,
    setItem: () => {
      throw new Error('preview must not write localStorage')
    },
    removeItem: () => {
      throw new Error('preview must not clear localStorage')
    },
  })
  Reflect.set(globalThis, 'document', {
    get cookie() {
      return ''
    },
    set cookie(_value: string) {
      throw new Error('preview must not write cookies')
    },
  })
}

afterEach(() => {
  Reflect.set(globalThis, 'localStorage', realLocalStorage)
  Reflect.set(globalThis, 'document', realDocument)
})

describe('unsaved site theme preview', () => {
  test('a non-default preset is mirrored onto the preview target attribute', () => {
    const { ops, target } = recordingTarget()

    qyApplyPresetPreview(target, 'steins-gate', 'default')

    assert.deepEqual(ops, [
      { kind: 'set', name: QY_PRESET_ATTRIBUTE, value: 'steins-gate' },
    ])
  })

  test('the upstream default removes the attribute instead of writing it literally', () => {
    // theme-presets.css 没有 [data-theme-preset='default'] 这条选择器，写进去
    // 得到的是一个没有任何预设变量的半成品页面，而不是「默认主题」。
    const { ops, target } = recordingTarget()

    qyApplyPresetPreview(target, 'default', 'default')

    assert.deepEqual(ops, [{ kind: 'remove', name: QY_PRESET_ATTRIBUTE }])
  })

  test('restoring after the draft is abandoned puts the personal preset back', () => {
    const { ops, target } = recordingTarget()

    qyApplyPresetPreview(target, 'ocean-breeze', 'default')
    qyApplyPresetPreview(target, 'anthropic', 'default')

    assert.deepEqual(ops.at(-1), {
      kind: 'set',
      name: QY_PRESET_ATTRIBUTE,
      value: 'anthropic',
    })
  })

  test('previewing writes neither localStorage nor cookies', () => {
    // 作用域契约：草稿只影响当前标签页。一旦预览把草稿写进
    // qy_site_theme_preset / qy_site_theme_force，管理员没点保存就走开也会让这台
    // 机器此后每次都用草稿主题，force 缓存还会连用户 cookie 偏好一起忽略。
    forbidPersistence()
    const { target } = recordingTarget()

    assert.doesNotThrow(() =>
      qyApplyPresetPreview(target, 'lavender-dream', 'default')
    )
    assert.doesNotThrow(() =>
      qyApplyPresetPreview(target, 'default', 'default')
    )
  })
})
