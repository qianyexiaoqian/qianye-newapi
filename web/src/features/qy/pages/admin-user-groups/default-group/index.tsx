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

import { QyConfirmDialog } from '../../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../../components/qy-page-boundary'
import { qyErrorMessage } from '../../../lib/api'
import { qyKeys } from '../../../lib/query-keys'
import { qySaveUserGroupConfig, qyUserGroupConfigQuery } from './api'
import type { QyUserGroupOption } from './types'

/**
 * 「新用户默认分组」卡片 —— 挂在系统设置 → 计费与支付 → 用户分组页上。
 *
 * ── 它为什么不再是一个独立页面 ──
 *
 * 项目方原话:「当前为何要有 2 个用户分组?只保留一个新的即可,旧的这个移除掉。」
 * 侧栏那一页(`/qy/admin/user-group`)整页只有下面这一个下拉,而它选的东西与本页
 * 那两张表列的是同一批用户分组名。两个入口、两份看起来都权威的清单,运营在其中
 * 一个里新建一档人之后到另一个里去找,找不到就会再建一次。
 *
 * ── 为什么是一张卡片(一个下拉),而不是每行一个「设为默认」标记 ──
 *
 * 行内标记表达不了这项配置的三分之二:
 *
 *  1. **「不设置」是一个合法且常用的取值**。它的含义是"让上游数据库默认值继续
 *     兜底",与"我选了 default"不是一回事(后者会在 default 被删掉时告警)。
 *     行内标记里的"不设置"只能表现为"一行都没标",而那与"还没加载出来"、
 *     "清单本身是空的"在屏幕上长得一模一样。
 *  2. **配置值可能不在任何一行里**。`configured_valid=false` 说的正是这件事:
 *     配好之后那个分组被删了。本页两张表的行分别来自 `users.group` 观测值与
 *     登记表,一个已被删掉的名字两边都没有 —— 没有行,就挂不上标记,而这恰好是
 *     最需要被看见的状态。
 *  3. **「实际生效」与「已配置」是两个值**。它们只在失效时不同,而那一刻必须
 *     两个都显示出来,否则运营会以为自己配的那个正在生效。
 *
 * 另外这项配置走的是自己的端点(`PUT /admin/user-group/config`,自带审计),
 * 不属于本页那次 `updateOption` 批量保存 —— 后者的键域被
 * `useGroupOptionSave<USER_GROUP_PAGE_KEYS>` 精确锁死,混进来会让"同一份数据只有
 * 一个编辑器"那条约束在编译期失效。所以它自带保存按钮与二次确认。
 *
 * ── 三件从旧页面原样搬过来、一件都不能省的事 ──
 *
 *  1. **下拉选项必须来自后端**。能不能选一个分组取决于它在不在
 *     「登记表 ∪ 分组倍率表」里,而那个并集只有后端看得到。前端自己列一遍必然
 *     与后端的校验口径漂移,表现为「下拉里能选、保存时报不存在」。
 *  2. **`has_channels` 为 false 必须当场警告**。那意味着该分组下没有任何启用的
 *     渠道,选它等于让新用户注册完一个模型都调不通,而且没有任何报错指向这里。
 *  3. **保存前二次确认**。后端每次保存都写审计,这里复述「从哪个改到哪个」。
 */
export function QyNewUserDefaultGroupCard() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  // `retry:false`:扩展未启用时这个端点直接 404,重试三次只会让卡片上的
  // 「加载中」赖着不走。QyPageBoundary 会把 404 渲染成中性空态而不是红色报错。
  const query = useQuery({ ...qyUserGroupConfigQuery(), retry: false })
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
    <Card data-card-hover='false'>
      <CardHeader className='border-b'>
        <CardTitle>{t('qy_ug_title')}</CardTitle>
        <CardDescription>{t('qy_ug_desc')}</CardDescription>
      </CardHeader>
      <CardContent className='space-y-4'>
        <QyPageBoundary query={query}>
          {config != null && (
            <div className='space-y-4'>
              {/*
                失效与探测失败摆在下拉**上面**：它们说的是"你现在看到的这个值
                其实没在生效"，放在保存按钮下方的话，运营已经改完并按下保存了
                才会读到。
              */}
              {!config.configured_valid && (
                <Alert variant='destructive'>
                  <TriangleAlert />
                  <AlertTitle>{t('qy_ug_stale_title')}</AlertTitle>
                  <AlertDescription>
                    {t('qy_ug_stale_desc', {
                      fallback: config.fallback_group,
                      group: config.default_group,
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

              <div className='grid gap-4 sm:grid-cols-[minmax(0,20rem)_minmax(0,1fr)] sm:items-start'>
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
                          {groupLabel(group, config.channels_probe_ok, t)}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <p className='text-muted-foreground text-xs'>
                    {t('qy_ug_field_hint', { group: config.fallback_group })}
                  </p>
                </div>

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
            </div>
          )}
        </QyPageBoundary>
      </CardContent>

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
    </Card>
  )
}

/**
 * 下拉项文案。
 *
 * 探测失败时不加叹号 —— 那时 `has_channels` 全是「不确定」，一律标红只会让人
 * 以为所有分组都坏了。
 *
 * 不再显示倍率：拆分之后 `ratio` 是这个名字在 `GroupRatio` 里的值，而那张表的键
 * 是**模型分组**。把它印在一个用户分组旁边，正是项目方点名要消掉的那处错位；
 * 只登记在 `qy_user_groups` 里的分组更是压根没有这个数。改成标出「未登记」——
 * 那才是运营在这个下拉里唯一需要区分的两类名字。
 */
function groupLabel(
  group: QyUserGroupOption,
  probeOK: boolean,
  t: (key: string) => string
): string {
  let label = group.name
  if (!group.registered) label += ` ${t('qy_ug_opt_unregistered')}`
  if (probeOK && !group.has_channels) label += ' ⚠'
  return label
}

function StatusRow(props: { label: string; value: string }) {
  return (
    <div className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
      <dt className='text-muted-foreground'>{props.label}</dt>
      <dd className='font-medium'>{props.value}</dd>
    </div>
  )
}
