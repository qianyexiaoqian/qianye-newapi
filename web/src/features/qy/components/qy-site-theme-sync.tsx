import { useQySiteTheme } from '../hooks/use-qy-site-theme'

/**
 * 把站点默认主题同步到当前会话。不渲染任何东西。
 *
 * 做成组件而不是直接在 __root 里调 hook:useQySiteTheme 依赖
 * ThemeCustomizationProvider 的 context,必须挂在 provider **内部**。
 * 而 __root 的组件体本身在 provider 外层,直接调用会拿到 FALLBACK_CONTEXT
 * (那是一组空实现),表现为"配置读到了但主题永远不变"。
 */
export function QySiteThemeSync() {
  useQySiteTheme()
  return null
}
