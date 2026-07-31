/**
 * 站点默认主题的解析与缓存。
 *
 * 背景:上游把默认预设硬编码在 DEFAULT_THEME_CUSTOMIZATION.preset,想换默认
 * 主题只能改代码重新构建。这里把它变成运营可在后台改的配置。
 *
 * 优先级(从高到低):
 *   1. 站点强制预设(管理员开了 force_preset)—— 忽略用户偏好
 *   2. 用户个人 cookie 偏好
 *   3. 站点默认预设
 *   4. 上游硬编码默认
 *
 * 为什么缓存进 localStorage:主题必须在首屏渲染之前确定,而站点配置要等
 * /api/qy/config 返回。首次访问不可避免会先按上游默认渲染再切换;把结果缓存
 * 下来,之后每次加载都是同步取值,不再闪烁。
 */

const SITE_PRESET_KEY = 'qy_site_theme_preset'
const SITE_FORCE_KEY = 'qy_site_theme_force'

function readLocal(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    // 隐私模式 / 存储被禁用时 localStorage 会抛异常。
    // 主题是展示层配置,拿不到就退回上游默认,绝不能让它把整个应用挂掉。
    return null
  }
}

function writeLocal(key: string, value: string) {
  try {
    localStorage.setItem(key, value)
  } catch {
    /* 同上,忽略 */
  }
}

/**
 * 解析首屏应当使用的预设。
 *
 * upstreamDefault 由调用方传入(而不是在这里 import),避免本文件依赖上游模块 ——
 * 上游那个常量若改名,编译期就会在调用点报错,而不是在这里静默失效。
 */
export function qyResolveDefaultPreset(upstreamDefault: string): string {
  const forced = readLocal(SITE_FORCE_KEY) === 'true'
  const sitePreset = readLocal(SITE_PRESET_KEY)

  // 强制模式:站点预设覆盖一切,连用户 cookie 也不认。
  if (forced && sitePreset) return sitePreset

  // 非强制:这里只负责"没有用户偏好时用什么"。用户 cookie 的优先级由
  // 上游的 readCookie 保证 —— 它拿本函数的返回值当 fallback。
  return sitePreset || upstreamDefault
}

/** 站点是否开启了强制预设。 */
export function qySiteThemeForced(): boolean {
  return readLocal(SITE_FORCE_KEY) === 'true'
}

/** 站点配置的默认预设(未配置时返回 null)。 */
export function qySitePreset(): string | null {
  return readLocal(SITE_PRESET_KEY)
}

/**
 * 把从 /api/qy/config 拿到的站点主题写进本地缓存。
 *
 * 返回值表示缓存是否发生了变化 —— 调用方据此决定要不要立即应用,
 * 避免每次接口返回都无谓地重设一次主题。
 */
export function qyCacheSiteTheme(preset: string, force: boolean): boolean {
  const changed =
    readLocal(SITE_PRESET_KEY) !== preset ||
    (readLocal(SITE_FORCE_KEY) === 'true') !== force
  writeLocal(SITE_PRESET_KEY, preset)
  writeLocal(SITE_FORCE_KEY, force ? 'true' : 'false')
  return changed
}
