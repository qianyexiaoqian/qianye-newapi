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
 * 只做一件事：Midnight Signal 主题下把 `<Title>` 的内容换成参考稿的三段式区段头
 * （等宽序号 `GATE 07` + 戳记大标题 + 大写英文页面代号）。
 *
 * 为什么包一层而不是改上游组件：
 *   - `components/layout/` 是上游文件，`channels/dashboard/keys/models` 四个上游
 *     页面也在用它。往里加 qy 的构图会让上游页面一起变形，并且每次合并上游都要
 *     手工解冲突。包一层之后 qy 侧的改动面是"每个页面换一行 import"。
 *   - 插槽标记（`SectionPageLayout.Title` 等）**原样再导出**，页面里的
 *     `<SectionPageLayout.Title>` 写法一个字都不用改 —— 上游用 `child.type ===`
 *     做插槽识别，共用同一个函数引用才能被认出来。
 *
 * 为什么条件渲染而不是纯 CSS 隐藏：序号与页面代号是**新增的文本内容**，
 * 不是既有元素的样式。切到别的预设时它们必须从 DOM 里消失（屏幕阅读器、
 * 页面搜索、复制粘贴都不该看到「GATE」），单靠 `display:none` 做不到，
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
              display 覆写完成，见 styles/qy-sg-apply.css §10。 */}
          <span className='qy-sg-page-head'>
            <span className='qy-sg-page-head-main'>
              <span className='qy-sg-no'>
                {t('qy_sg_stamp_no', { no: meta.no })}
              </span>
              <span className='qy-sg-sec-title'>{title}</span>
            </span>
            {meta.codeKey != null && (
              // 装饰性字形，不参与朗读：屏幕阅读器把一串大写英文代号混进中文
              // 页面标题里只会制造噪声。
              <span className='qy-sg-code' aria-hidden='true'>
                {t(meta.codeKey)}
              </span>
            )}
          </span>
        </SectionPageLayout.Title>
      )
    })
  }, [props.children, steinsGate, pathname, t])

  // 上游 SectionPageLayout 只认四类插槽（Title/Actions/Content/Breadcrumb），
  // **其余 children 一律丢弃** —— 它的 Children.forEach 只把认识的那几个赋给
  // 局部变量，认不出的连引用都不保留。
  //
  // 而 qy 的页面普遍把弹窗写在插槽外面：
  //
  //     <QySectionPageLayout.Content>…</QySectionPageLayout.Content>
  //     <QyGroupRuleFormSheet … />      ← 这一行从来没有被渲染过
  //     <QyConfirmDialog … />
  //
  // 后果是这些页面上所有的确认框、编辑抽屉、详情弹窗全是死的：点击按钮确实改了
  // state，但组件压根不在 DOM 里，什么都不会发生。实测受影响 14 个页面。
  // 「划转分组点新建规则没反应」就是它的一个表现。
  //
  // 在这里把非插槽 children 收集出来、与 SectionPageLayout 并列渲染。
  // 弹窗类组件本身都走 portal 渲染到 body，挂在哪个父节点下不影响表现。
  //
  // 为什么不逐页把弹窗塞进 <Content> 里：那是 14 个文件的改动，而且下一个新页面
  // 照着现有页面抄，还会再犯一次 —— 这正是本项目累计出现十几次的「断链」形状。
  // 修在唯一的公共外壳上，新页面天然不会踩。
  const extras = useMemo(() => {
    const slots = new Set<unknown>([
      SectionPageLayout.Title,
      SectionPageLayout.Actions,
      SectionPageLayout.Content,
      SectionPageLayout.Breadcrumb,
    ])
    const rest: React.ReactNode[] = []
    // `children` 在 steinsGate 分支下是 Children.map 的产物（已归一），
    // 非 steinsGate 分支下是原始的 props.children —— 后者的类型里含 `false`
    // （页面里 `{cond && <Dialog/>}` 的写法），而 Children.forEach 的签名不收它。
    // 转成 ReactNode 走一遍即可，运行时行为不变。
    Children.forEach(children as React.ReactNode, (node) => {
      // 非元素（字符串/数字/null）本来也会被上游丢掉，但它们出现在这里
      // 基本都是排版空白，捞回来反而会渲染出多余文本，故只收元素。
      if (!isValidElement(node)) return
      if (slots.has(node.type)) return
      rest.push(node)
    })
    return rest
  }, [children])

  return (
    <>
      <SectionPageLayout fixedContent={props.fixedContent}>
        {children}
      </SectionPageLayout>
      {extras}
    </>
  )
}

QySectionPageLayout.Title = SectionPageLayout.Title
QySectionPageLayout.Actions = SectionPageLayout.Actions
QySectionPageLayout.Content = SectionPageLayout.Content
QySectionPageLayout.Breadcrumb = SectionPageLayout.Breadcrumb
