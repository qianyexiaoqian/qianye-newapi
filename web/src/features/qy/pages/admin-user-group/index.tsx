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
import { Info, TriangleAlert } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import { qySaveUserGroupConfig, qyUserGroupConfigQuery } from './api'
import type { QyUserGroupOption } from './types'

/**
 * 新用户默认分组配置页。
 *
 * 只有一个设置项，但它决定「此后每一个注册用户能不能用模型」，因此三件事不能省：
 *
 *  1. **下拉选项必须来自后端**。分组不是实体表，只是散落在倍率表里的字符串 key；
 *     前端自己列一遍必然与后端校验口径漂移，表现为「下拉里能选、保存时报不存在」。
 *  2. **`has_channels` 为 false 必须当场警告**。那意味着该分组下没有任何启用的
 *     渠道，选它等于让新用户注册完一个模型都调不通，而且没有任何报错指向这里。
 *  3. **保存前二次确认**。后端每次保存都写审计，这里复述「从哪个改到哪个」。
 */
export function QyAdminUserGroup() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qyUserGroupConfigQuery())
  const config = query.data

  // 「不配置」这一项。空串在 Select 里不能当 value（Base UI 会把它当作未选中），
  // 所以用一个不可能与分组重名的哨兵值，提交前再映射回空串。
  const UNSET = '__qy_unset__'

  const [draft, setDraft] = useState<string>(UNSET)
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别的管理员改过之后重新取到）时重置草稿：
  // 保留旧草稿会让人基于过期基线做修改，把别人刚改的值又覆盖回去。
  useEffect(() => {
    if (config == null) return
    setDraft(config.default_group === '' ? UNSET : config.default_group)
  }, [config])

  const saveMutation = useMutation({
    mutationFn: qySaveUserGroupConfig,
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_ug_saved'))
      await queryClient.invalidateQueries({
        queryKey: qyKeys.adminUserGroupConfig(),
      })
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const target = draft === UNSET ? '' : draft
  const dirty = config != null && target !== config.default_group
  const selected = config?.groups.find((group) => group.name === target)
  const noChannels =
    config != null &&
    config.channels_probe_ok &&
    selected != null &&
    !selected.has_channels

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_user_group')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {config != null && (
            <div className='grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] lg:items-start'>
              <Card data-card-hover='false'>
                <CardHeader>
                  <CardTitle>{t('qy_ug_title')}</CardTitle>
                  <CardDescription>{t('qy_ug_desc')}</CardDescription>
                </CardHeader>
                <CardContent className='space-y-4'>
                  <div className='space-y-1.5'>
                    <Label htmlFor='qy-ug-select'>{t('qy_ug_field')}</Label>
                    <Select
                      value={draft}
                      onValueChange={(value) => setDraft(value ?? UNSET)}
                    >
                      <SelectTrigger id='qy-ug-select' className='w-full'>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        <SelectItem value={UNSET}>
                          {t('qy_ug_unset', { group: config.fallback_group })}
                        </SelectItem>
                        {config.groups.map((group) => (
                          <SelectItem key={group.name} value={group.name}>
                            {groupLabel(group, config.channels_probe_ok)}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <p className='text-muted-foreground text-xs'>
                      {t('qy_ug_field_hint', { group: config.fallback_group })}
                    </p>
                  </div>

                  {noChannels && (
                    <Alert variant='destructive'>
                      <TriangleAlert />
                      <AlertTitle>{t('qy_ug_no_channel_title')}</AlertTitle>
                      <AlertDescription>
                        {t('qy_ug_no_channel_desc')}
                      </AlertDescription>
                    </Alert>
                  )}

                  <Button
                    disabled={!dirty || saveMutation.isPending}
                    onClick={() => setConfirmOpen(true)}
                  >
                    {t('qy_ug_save')}
                  </Button>
                </CardContent>
              </Card>

              <Card data-card-hover='false'>
                <CardHeader>
                  <CardTitle>{t('qy_ug_status_title')}</CardTitle>
                  <CardDescription>{t('qy_ug_status_desc')}</CardDescription>
                </CardHeader>
                <CardContent className='space-y-3'>
                  {!config.configured_valid && (
                    <Alert variant='destructive'>
                      <TriangleAlert />
                      <AlertTitle>{t('qy_ug_stale_title')}</AlertTitle>
                      <AlertDescription>
                        {t('qy_ug_stale_desc', {
                          group: config.default_group,
                          fallback: config.fallback_group,
                        })}
                      </AlertDescription>
                    </Alert>
                  )}
                  {!config.channels_probe_ok && (
                    <Alert>
                      <Info />
                      <AlertTitle>{t('qy_ug_probe_failed_title')}</AlertTitle>
                      <AlertDescription>
                        {t('qy_ug_probe_failed_desc')}
                      </AlertDescription>
                    </Alert>
                  )}
                  <dl className='divide-border divide-y text-sm'>
                    <StatusRow
                      label={t('qy_ug_status_configured')}
                      value={
                        config.default_group === ''
                          ? t('qy_ug_status_none')
                          : config.default_group
                      }
                    />
                    <StatusRow
                      label={t('qy_ug_status_effective')}
                      value={config.effective_group}
                    />
                  </dl>
                </CardContent>
              </Card>
            </div>
          )}
        </QyPageBoundary>
      </QySectionPageLayout.Content>

      <QyConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={t('qy_ug_confirm_title')}
        description={t('qy_ug_confirm_desc')}
        confirmText={t('qy_ug_save')}
        isLoading={saveMutation.isPending}
        details={
          <p className='text-sm'>
            <span className='text-muted-foreground'>
              {config?.default_group === ''
                ? t('qy_ug_status_none')
                : config?.default_group}
            </span>
            {' → '}
            <strong>{target === '' ? t('qy_ug_status_none') : target}</strong>
          </p>
        }
        onConfirm={() => saveMutation.mutate(target)}
      />
    </QySectionPageLayout>
  )
}

/**
 * 下拉项文案。探测失败时不加叹号 —— 那时 `has_channels` 全是「不确定」，
 * 一律标红只会让人以为所有分组都坏了。
 */
function groupLabel(group: QyUserGroupOption, probeOK: boolean): string {
  const suffix = probeOK && !group.has_channels ? ' ⚠' : ''
  return `${group.name} (×${group.ratio})${suffix}`
}

function StatusRow(props: { label: string; value: string }) {
  return (
    <div className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
      <dt className='text-muted-foreground'>{props.label}</dt>
      <dd className='font-medium'>{props.value}</dd>
    </div>
  )
}
