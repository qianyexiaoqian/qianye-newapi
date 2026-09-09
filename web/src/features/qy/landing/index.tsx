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
import { useTranslation } from 'react-i18next'

import { PublicLayout } from '@/components/layout'
import { Footer } from '@/components/layout/components/footer'
import { Home } from '@/features/home'
import { useHomePageContent } from '@/features/home/hooks'
import { useAuthStore } from '@/stores/auth-store'

import { QyLandingApps } from './components/apps'
import { QyLandingCloser } from './components/closer'
import { QyLandingHero } from './components/hero'
import { QyLandingLineup } from './components/lineup'
import { QyLandingPromises } from './components/promises'

/**
 * 未登录首页（路由 `/`）。
 *
 * ── 为什么另起一个页面而不是改 `features/home` ──
 *
 * `features/home/` 是上游文件，而首页恰好是上游改动最频繁的目录之一。上一版
 * 的做法是「保留上游构图 + 用 i18n 覆盖英文原文换掉文案 + 用主题 CSS 去色」，
 * 三层补丁叠在一起，结果仍然是**上游那一页**：首屏左字右终端、上下描边的统计
 * 横带、bento 宫格、三步上手圆角图标。项目方的原话是「和原项目太过雷同」。
 *
 * 换个接法之后 `features/home/` 一行不改，合并上游时冲突面仍然为 0，而版式
 * 完全由本目录自己说了算。接缝只有 `routes/index.tsx` 的一行 import。
 *
 * ── 管理员自定义首页仍然优先 ──
 *
 * 后台「设置 → 首页内容」配了 Markdown / HTML / URL 时，那份内容永远赢。
 * 这条分支直接交回上游的 `<Home>`（它已经处理好 iframe 沙箱、HTML 隔离、
 * Markdown 渲染与 localStorage 预热四种情况），本页只接管**没配**的默认形态 ——
 * 把那四种情况在这里抄一遍，就是给自己造一个必然漂移的副本。
 */
export function QyLanding() {
  const { t } = useTranslation()
  const { content, isLoaded } = useHomePageContent()
  const isAuthenticated = !!useAuthStore((state) => state.auth.user)

  if (!isLoaded) {
    return (
      <PublicLayout showMainContainer={false}>
        <main className='flex min-h-screen items-center justify-center'>
          <div className='text-muted-foreground'>{t('Loading...')}</div>
        </main>
      </PublicLayout>
    )
  }

  if (content) return <Home />

  return (
    <PublicLayout showMainContainer={false}>
      <QyLandingHero isAuthenticated={isAuthenticated} />
      <QyLandingPromises />
      <QyLandingLineup />
      <QyLandingApps />
      <QyLandingCloser isAuthenticated={isAuthenticated} />
      <Footer />
    </PublicLayout>
  )
}
