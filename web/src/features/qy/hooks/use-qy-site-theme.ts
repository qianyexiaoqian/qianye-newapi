import { useEffect } from 'react'

import { useThemeCustomization } from '@/context/theme-customization-provider'
import {
  THEME_PRESET_VALUES,
  type ThemePreset,
} from '@/lib/theme-customization'

import { useQyConfig } from './use-qy-config'
import {
  qyCacheSiteTheme,
  qySitePreset,
  qySiteThemeForced,
} from '../lib/site-theme'

/**
 * 把后端下发的站点默认主题同步到本地并按需应用。
 *
 * 时序说明:主题必须在首屏渲染之前确定,而站点配置要等 /api/qy/config 返回。
 * 因此走两步 —— 缓存进 localStorage 供下次同步取用(见 site-theme.ts),
 * 本次则在配置到达后按下面的规则决定要不要立即切换。
 *
 * 只在这两种情况下主动改用户的主题:
 *   1. 站点开了强制模式 —— 忽略个人偏好
 *   2. 站点默认变了,且用户从未表达过个人偏好
 *
 * 绝不在用户已经自己选过主题时覆盖他 —— 那是把"默认值"变成"强制值"。
 */
export function useQySiteTheme() {
  const config = useQyConfig()
  const { customization, setPreset } = useThemeCustomization()

  const preset = config.theme?.default_preset
  const force = config.theme?.force_preset ?? false

  useEffect(() => {
    if (!preset) return
    // 后端下发了一个前端不认识的预设(版本不一致)时按兵不动:
    // 应用一个没有样式定义的预设会得到一个半成品页面,比不生效更糟。
    if (!THEME_PRESET_VALUES.has(preset as ThemePreset)) return

    const hadSitePreset = qySitePreset()
    const wasForced = qySiteThemeForced()
    qyCacheSiteTheme(preset, force)

    if (force) {
      if (customization.preset !== preset) {
        setPreset(preset as ThemePreset)
      }
      return
    }

    // 刚从强制模式切回非强制:不回滚用户当前看到的主题。
    // 用户此刻看到的就是站点主题,悄悄换掉只会让人困惑。
    if (wasForced) return

    // 首次拿到站点默认(本地还没缓存过),而用户没有个人偏好 —— 应用它。
    // 判据用"本地缓存是否为空"而不是"cookie 是否为空":用户可能主动选了
    // 与上游默认相同的预设,那种情况下 cookie 会被上游逻辑删掉。
    if (!hadSitePreset && !document.cookie.includes('theme_preset=')) {
      if (customization.preset !== preset) {
        setPreset(preset as ThemePreset)
      }
    }
  }, [preset, force, customization.preset, setPreset])
}
