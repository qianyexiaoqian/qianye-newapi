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

import { Badge } from '@/components/ui/badge'
import { Markdown } from '@/components/ui/markdown'
import { cn } from '@/lib/utils'

import { formatQyTs } from '../../ops/format'
import type { QyTicketMessage } from '../types'
import { QyTicketImage } from './ticket-image'

/**
 * 工单对话。用户端与管理端共用 —— 要展示的是同一件事，两边各写一份必然漂移成
 * "管理端能看到附图、用户端看不到"这类不对称。
 *
 * ## 正文为什么走 `Markdown` 而不是自己渲染
 *
 * 需求要求正文支持 Markdown。仓库里已经有一条现成的、经过审阅的管线
 * （`components/ui/markdown`：marked 解析 → `DOMPurify.sanitize` 净化 →
 * 外链补 `target=_blank rel=noopener`），公告页与更新日志都在用它。
 *
 * **净化只有这一处，而且必须只有这一处。** 工单正文是用户写的、给管理员看的，
 * 是这个站点里最典型的"跨信任边界的富文本" —— 后端刻意原样存 Markdown 源码、
 * 不做任何 HTML 转换，正是为了不产生第二份能绕过这个净化器的内容。
 * 因此这里**绝不允许**改成 `dangerouslySetInnerHTML`，也不要在别处先把
 * body 转成 HTML 再传进来。
 *
 * `breaks` 开着：工单是聊天式的，用户按回车换行时期待真的换行，
 * 而标准 Markdown 会把单个换行折叠掉。
 */
export function QyTicketThread(props: {
  messages: QyTicketMessage[]
  /** 管理端下载图片走 /admin 前缀，用户端走用户前缀。 */
  scope?: 'admin' | 'user'
}) {
  const { t } = useTranslation()
  const scope = props.scope ?? 'user'

  return (
    <ol className='space-y-3'>
      {props.messages.map((message) => {
        const fromUser = message.author_type === 'user'
        // 内部备注用告警底色：管理员必须一眼看出"这条用户看不见"。靠一个小徽章
        // 是不够的 —— 长对话里徽章会被扫过去，而把内部判断误当成已发给用户的
        // 答复是这一页最贵的误读。
        let tone = 'bg-background'
        if (message.internal) tone = 'border-warning/50 bg-warning/10'
        else if (fromUser) tone = 'bg-muted/40'
        return (
          <li key={message.id} className={cn('rounded-md border p-3', tone)}>
            <div className='mb-1.5 flex flex-wrap items-center gap-2'>
              <span className='text-sm font-medium'>
                {fromUser
                  ? message.author_name || t('qy_tk_author_user')
                  : t('qy_tk_author_staff')}
              </span>
              {/* 管理员真名只在管理端有值（用户端后端不下发），所以这里
                  直接渲染即可，不需要再判一次"我是不是管理员"。 */}
              {!fromUser && message.author_name !== '' && (
                <span className='text-muted-foreground text-xs'>
                  {message.author_name}
                </span>
              )}
              {message.internal && (
                <Badge variant='outline'>{t('qy_tk_internal_note')}</Badge>
              )}
              <span className='text-muted-foreground ml-auto text-xs'>
                {formatQyTs(message.created_at)}
              </span>
            </div>

            {/* `untrusted` 不能省：这条正文是**任何注册用户**写的，而这段
                组件同时长在客服的处理台里。默认那份净化配置是给公告 / 更新日志
                （管理员自己写的内容）设计的，它放行 form/input/button、任意
                style 与外链图片 —— 在工单这条"用户 → 管理员浏览器"的通道上，
                那分别是"客服屏幕上的全屏假登录框"与"客服打开工单的已读回执 +
                出口 IP"。全站没有 CSP，这个开关就是唯一的防线。 */}
            <Markdown breaks untrusted className='text-sm'>
              {message.body}
            </Markdown>

            {message.attachments.length > 0 && (
              <div className='mt-2 flex flex-wrap gap-2'>
                {message.attachments.map((attachment) => (
                  <QyTicketImage
                    key={attachment.ref}
                    imageRef={attachment.ref}
                    scope={scope}
                  />
                ))}
              </div>
            )}
          </li>
        )
      })}
    </ol>
  )
}
