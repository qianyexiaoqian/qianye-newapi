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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Save } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Markdown } from '@/components/ui/markdown'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import {
  putQyRestrictedNotice,
  qyAdminRestrictedNoticeQueryOptions,
} from '../../../lib/restricted-notice'

/**
 * 受限账号公告的配置面。
 *
 * ## 它为什么从「系统设置 → 内容管理」搬到这里
 *
 * 项目方原话：「受限制账号，在系统设置里面单独进行配置。」
 *
 * 上一轮它挨着站点公告，理由是"同为面向用户的站点文案"。那条理由本身没错，
 * 但它回答的是**文案归谁管**，而项目方问的是**受限账号归哪一页管**。按前者
 * 归类的后果是：管理员想弄清"受限到底限了什么、现在有几个人被限制、他还能
 * 提工单吗"，得在内容管理、用户列表、Go 源码三处各看一眼，而三处都不承认
 * 自己是"受限账号"那一页。现在这三件事在同一屏上。
 *
 * ## 存的不是上游 options
 *
 * 这一节走 `/api/qy/admin/restricted-notice`，落在扩展库的 `qy_settings` 里，
 * 因此自带保存按钮 —— 它从来就没有接进上游那个 option 批量写入表单，
 * 搬家之后这一点不变。
 */
export function QyRestrictedNoticeCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyAdminRestrictedNoticeQueryOptions())

  const [enabled, setEnabled] = useState(false)
  const [title, setTitle] = useState('')
  const [body, setBody] = useState('')

  // 取数落定后灌一次初值。依赖是 `query.data` 本身，因此重新取数（保存后失效）
  // 会把表单同步到服务端的最新值，而用户输入期间不会被覆盖（data 引用不变）。
  useEffect(() => {
    if (query.data == null) return
    setEnabled(query.data.enabled)
    setTitle(query.data.title)
    setBody(query.data.body)
  }, [query.data])

  const titleMax = query.data?.title_max_runes ?? 0
  const bodyMax = query.data?.body_max_runes ?? 0
  // 按码点数,与后端的 rune 口径一致。用 `.length` 会把一个 emoji 算成 2,
  // 于是计数器说超了、后端说没超。
  const titleUsed = [...title].length
  const bodyUsed = [...body].length
  const overLimit =
    (titleMax > 0 && titleUsed > titleMax) ||
    (bodyMax > 0 && bodyUsed > bodyMax)
  // 开着却没内容 = 受限用户首屏上一块空白卡片。后端会 400，这里先把保存按钮
  // 禁掉并给出理由，免得运营对着一句「公告正文过长」之外的报错猜自己漏了什么。
  const incomplete = enabled && (title.trim() === '' || body.trim() === '')

  const save = useMutation({
    mutationFn: () =>
      putQyRestrictedNotice({
        enabled,
        title: title.trim(),
        body: body.trim(),
      }),
    onSuccess: () => {
      toast.success(t('qy_restricted_notice_saved'))
      // 管理端与用户端两个 key 都要冲：管理员本人如果正被限制着（不可能，但
      // 缓存不该依赖这个假设），横幅上那段应当立刻跟上。
      void queryClient.invalidateQueries({
        queryKey: qyKeys.adminRestrictedNotice(),
      })
      void queryClient.invalidateQueries({
        queryKey: qyKeys.restrictedNotice(),
      })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  return (
    <Card>
      <CardHeader>
        <CardTitle>{t('qy_restricted_notice_title')}</CardTitle>
        <CardDescription>{t('qy_restricted_notice_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <div className='flex items-center justify-between gap-4 rounded-lg border p-4'>
          <div className='space-y-1'>
            <Label htmlFor='qy-restricted-notice-enabled'>
              {t('qy_restricted_notice_enabled')}
            </Label>
            <p className='text-muted-foreground text-sm'>
              {t('qy_restricted_notice_enabled_hint')}
            </p>
          </div>
          <Switch
            id='qy-restricted-notice-enabled'
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>

        <div className='space-y-2'>
          <Label htmlFor='qy-restricted-notice-title'>
            {t('qy_restricted_notice_field_title')}
          </Label>
          <Input
            id='qy-restricted-notice-title'
            value={title}
            onChange={(event) => setTitle(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {titleUsed} / {titleMax}
          </p>
        </div>

        <div className='space-y-2'>
          <Label htmlFor='qy-restricted-notice-body'>
            {t('qy_restricted_notice_field_body')}
          </Label>
          <Textarea
            id='qy-restricted-notice-body'
            rows={8}
            value={body}
            onChange={(event) => setBody(event.target.value)}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_restricted_notice_markdown_hint')} · {bodyUsed} / {bodyMax}
          </p>
        </div>

        {body.trim() !== '' && (
          <div className='space-y-2'>
            <Label>{t('qy_restricted_notice_preview')}</Label>
            {/*
              预览必须与用户端**同一档**净化（`untrusted`），否则运营会在这里
              看到一段能用、上线后被净化掉一半的内容 —— 一个所见非所得的编辑器
              比没有预览更糟。
            */}
            <div className='rounded-lg border p-4'>
              <p className='font-medium'>{title}</p>
              <Markdown breaks untrusted className='mt-1'>
                {body}
              </Markdown>
            </div>
          </div>
        )}

        {incomplete && (
          <p className='text-destructive text-sm'>
            {t('qy_restricted_notice_incomplete')}
          </p>
        )}

        <div>
          <Button
            onClick={() => save.mutate()}
            disabled={save.isPending || overLimit || incomplete}
          >
            <Save className='size-4' />
            {t('Save')}
          </Button>
        </div>
      </CardContent>
    </Card>
  )
}
