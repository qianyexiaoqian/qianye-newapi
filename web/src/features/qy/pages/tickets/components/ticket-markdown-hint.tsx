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

/**
 * "正文支持 Markdown"的一行提示。
 *
 * 单独成组件是因为它出现在三处（新建、用户回复、管理员回复），而它要说的是同
 * 一件事。三处各写一遍的结果是其中一处日后被改成别的说法，用户便会以为回复框
 * 和新建框支持的语法不一样。
 *
 * 刻意**不做**实时预览：预览要么再引一个渲染实例（同一段内容两处渲染，
 * 净化配置迟早漂移），要么把编辑器整体换成富文本（那会让正文不再是 Markdown
 * 源码，后端存的东西就变了）。提交后立刻能在对话里看到渲染结果，
 * 这个反馈环已经足够短。
 */
export function QyTicketMarkdownHint() {
  const { t } = useTranslation()
  return (
    <p className='text-muted-foreground text-xs'>{t('qy_tk_markdown_hint')}</p>
  )
}
