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
import { useLocation } from '@tanstack/react-router'
import { Children, isValidElement, useMemo, type ReactElement } from 'react'
import { useTranslation } from 'react-i18next'

import {
  SectionPageLayout,
  type SectionPageLayoutProps,
} from '@/components/layout'

import { useQyIsSteinsGate } from '../hooks/use-qy-theme-preset'
import { qyPageMeta } from '../lib/page-meta'

/**
 * qy 页面的统一外壳 —— 在上游 `SectionPageLayout` 之外套一层，
 * 只做一件事：Steins Gate 主题下把 `<Title>` 的内容换成参考稿的三段式区段头
 * （等宽序号 `LAB MEMO — 07` + 衬线大标题 + 日文副标）。
 *
 * 为什么包一层而不是改上游组件：
 *   - `components/layout/` 是上游文件，`channels/dashboard/keys/models` 四个上游
 *     页面也在用它。往里加 qy 的构图会让上游页面一起变形，并且每次合并上游都要
 *     手工解冲突。包一层之后 qy 侧的改动面是"每个页面换一行 import"。
 *   - 插槽标记（`SectionPageLayout.Title` 等）**原样再导出**，页面里的
 *     `<SectionPageLayout.Title>` 写法一个字都不用改 —— 上游用 `child.type ===`
 *     做插槽识别，共用同一个函数引用才能被认出来。
 *
 * 为什么条件渲染而不是纯 CSS 隐藏：序号与日文副标是**新增的文本内容**，
 * 不是既有元素的样式。切到别的预设时它们必须从 DOM 里消失（屏幕阅读器、
 * 页面搜索、复制粘贴都不该看到「LAB MEMO」），单靠 `display:none` 做不到，
 * 而在预设作用域外写规则又违反规范 §5.1。
 */
export function QySectionPageLayout(props: SectionPageLayoutProps) {
  const { t } = useTranslation()
  const pathname = useLocation({ select: (location) => location.pathname })
  const steinsGate = useQyIsSteinsGate()

  const children = useMemo(() => {
    if (!steinsGate) return props.children
    const meta = qyPageMeta(pathname)

    return Children.map(props.children, (node) => {
      if (!isValidElement(node)) return node
      if (node.type !== SectionPageLayout.Title) return node
      const title = (node as ReactElement<{ children?: React.ReactNode }>).props
        .children

      return (
        <SectionPageLayout.Title>
          {/* 全部用 <span>：这段会被上游放进 <h2> 里，而 <h2> 只接受
              phrasing content，塞 <div> 是非法嵌套。排版由 CSS 的
              display 覆写完成，见 styles/qy-sg-pages.css。 */}
          <span className='qy-sg-page-head'>
            <span className='qy-sg-page-head-main'>
              <span className='qy-sg-no'>
                {t('qy_sg_lab_memo', { no: meta.no })}
              </span>
              <span className='qy-sg-sec-title'>{title}</span>
            </span>
            {meta.jpKey != null && (
              // 装饰性字形，不参与朗读：屏幕阅读器把日文假名混进中文页面标题里
              // 只会制造噪声。
              <span className='qy-sg-jp' aria-hidden='true'>
                {t(meta.jpKey)}
              </span>
            )}
          </span>
        </SectionPageLayout.Title>
      )
    })
  }, [props.children, steinsGate, pathname, t])

  return (
    <SectionPageLayout fixedContent={props.fixedContent}>
      {children}
    </SectionPageLayout>
  )
}

QySectionPageLayout.Title = SectionPageLayout.Title
QySectionPageLayout.Actions = SectionPageLayout.Actions
QySectionPageLayout.Content = SectionPageLayout.Content
QySectionPageLayout.Breadcrumb = SectionPageLayout.Breadcrumb
