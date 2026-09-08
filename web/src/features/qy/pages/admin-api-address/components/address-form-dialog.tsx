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
import { useQuery } from '@tanstack/react-query'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { MultiSelect } from '@/components/multi-select'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { getUserGroupOptions } from '@/features/users/api'
import { getBgColorClass } from '@/lib/colors'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import type { QyApiAddress, QyApiAddressUpsert } from '../types'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** 为空表示新建。 */
  current: QyApiAddress | null
  isPending: boolean
  onSubmit: (body: QyApiAddressUpsert) => void
}

/**
 * 颜色调色板，与后端 `normalizeColor` 的白名单同一份清单（上游「API信息」
 * 面板的 14 色）。UI 里的「默认」用哨兵值 'default' 表示——Select 的空串
 * 选项与 placeholder 语义纠缠，提交时再映射回空串。
 */
const COLOR_VALUES = [
  'blue',
  'green',
  'cyan',
  'purple',
  'pink',
  'red',
  'orange',
  'amber',
  'yellow',
  'lime',
  'teal',
  'indigo',
  'violet',
  'slate',
] as const

const DEFAULT_COLOR_SENTINEL = 'default'

/**
 * 展示位置，与后端 `allowedSurfaces` 同一份清单。UI 上两个都勾 = 存空串
 * （所有位置可见，也是存量行与缺省的语义）；一个都不勾没有意义 —— 那是
 * 「停用」该干的事 —— 所以提交按钮会被摁住。
 */
const SURFACE_VALUES = ['console', 'picker'] as const

/**
 * 新建 / 编辑一条 API 地址。
 *
 * ── 为什么校验只做「必填 + 空白」这一层 ──
 * URL 的合法性判据（scheme 白名单、不许带凭据、不许带查询串、尾斜杠归一化）
 * 全部在后端 `normalizeURL` 里，前端**刻意不复述**：复述一遍就是同一份规则的
 * 第二份拷贝，两份迟早漂移，而漂移的方向必然是前端更松（后端加了新判据前端
 * 不会跟）或更严（前端挡住了后端本来接受的写法，用户完全无从申诉）。
 * 这里只挡「按钮点了什么都没发生」这一种体验问题，其余交给后端的 code → 文案。
 */
export function QyApiAddressFormDialog(props: Props) {
  const { t } = useTranslation()
  const formId = useId()
  const [name, setName] = useState('')
  const [remark, setRemark] = useState('')
  const [url, setUrl] = useState('')
  const [userGroups, setUserGroups] = useState<string[]>([])
  const [surfaces, setSurfaces] = useState<string[]>([...SURFACE_VALUES])
  const [color, setColor] = useState('')
  const [enabled, setEnabled] = useState(true)

  const current = props.current
  // 每次打开都从入参重置：留着上一次的草稿会让「编辑 A → 关掉 → 新建」带出
  // A 的内容，而那看起来完全像是新建表单的默认值。
  useEffect(() => {
    if (!props.open) return
    setName(current?.name ?? '')
    setRemark(current?.remark ?? '')
    setUrl(current?.url ?? '')
    setUserGroups(
      (current?.user_groups ?? '').split(',').filter((group) => group !== '')
    )
    // 存储侧空串 = 所有位置可见，回填成"两个都勾"——弹窗里没有"什么都不勾"
    // 这个合法状态，空串必须显式展开。
    const storedSurfaces = (current?.surfaces ?? '')
      .split(',')
      .filter((surface) => surface !== '')
    setSurfaces(
      storedSurfaces.length === 0 ? [...SURFACE_VALUES] : storedSurfaces
    )
    setColor(current?.color ?? '')
    setEnabled(current?.enabled ?? true)
  }, [props.open, current])

  // 用户分组候选。刻意用 `/api/user-group/options`（users.group 的 distinct）
  // 而不是模型分组那一份 —— 本仓把两种分组拆成了两个命名空间（见 groupns），
  // 地址可见性按的是**用户分组**。queryKey 与订阅套餐编辑那边共用同一份缓存。
  const userGroupsQuery = useQuery({
    queryKey: ['user-group-options'],
    queryFn: () => getUserGroupOptions(),
    staleTime: 5 * 60 * 1000,
    enabled: props.open,
  })
  // 存量地址绑定的分组可能已经一个用户都没有了，于是不在候选清单里 ——
  // 必须把当前值补进去，否则运营只想改个备注，一打开分组就自己少了几个。
  const groupOptions = [
    ...new Set([...(userGroupsQuery.data?.data ?? []), ...userGroups]),
  ].map((group) => ({ label: group, value: group }))

  const canSubmit =
    name.trim() !== '' &&
    url.trim() !== '' &&
    surfaces.length > 0 &&
    !props.isPending

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={current == null ? t('qy_aa_create') : t('qy_aa_edit')}
      description={t('qy_aa_form_desc')}
      contentClassName='sm:max-w-lg'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('qy_aa_cancel')}
          </Button>
          <Button form={formId} type='submit' disabled={!canSubmit}>
            {t('qy_aa_save')}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className='space-y-4'
        onSubmit={(event) => {
          event.preventDefault()
          if (!canSubmit) return
          props.onSubmit({
            name: name.trim(),
            remark: remark.trim(),
            url: url.trim(),
            user_groups: userGroups.join(','),
            // 两个都勾 = 存空串（所有位置可见）：全集与空串今天等价，但存
            // 空串让"将来新增第三个位置"时这些行自动跟上，而存全集会把它们
            // 冻结在今天的两个位置上。
            surfaces:
              surfaces.length === SURFACE_VALUES.length
                ? ''
                : surfaces.join(','),
            color,
            enabled,
          })
        }}
      >
        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-name`}>{t('qy_aa_field_name')}</Label>
          <Input
            id={`${formId}-name`}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t('qy_aa_field_name_ph')}
          />
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-url`}>{t('qy_aa_field_url')}</Label>
          <Input
            id={`${formId}-url`}
            value={url}
            onChange={(event) => setUrl(event.target.value)}
            placeholder='https://api.example.com'
            autoComplete='off'
            spellCheck={false}
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_aa_field_url_hint')}
          </p>
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-remark`}>{t('qy_aa_field_remark')}</Label>
          <Input
            id={`${formId}-remark`}
            value={remark}
            onChange={(event) => setRemark(event.target.value)}
            placeholder={t('qy_aa_field_remark_ph')}
          />
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-groups`}>{t('qy_aa_field_groups')}</Label>
          <MultiSelect
            id={`${formId}-groups`}
            options={groupOptions}
            selected={userGroups}
            onChange={setUserGroups}
            placeholder={t('qy_aa_field_groups_ph')}
            allowCreate
            // 传 key 而不是 t(key)：MultiSelect 内部自己 t(createLabel, {value})，
            // 在这里先翻译会把 {{value}} 占位符按缺失变量吃成空串。
            createLabel='qy_aa_field_groups_create'
          />
          <p className='text-muted-foreground text-xs'>
            {t('qy_aa_field_groups_hint')}
          </p>
        </div>

        <div className='space-y-1.5'>
          <Label>{t('qy_aa_field_surfaces')}</Label>
          <div className='space-y-2 rounded-lg border p-3'>
            {SURFACE_VALUES.map((surface) => (
              <div key={surface} className='flex items-center gap-2'>
                <Checkbox
                  id={`${formId}-surface-${surface}`}
                  checked={surfaces.includes(surface)}
                  onCheckedChange={(checked) =>
                    setSurfaces((prev) =>
                      checked === true
                        ? [...prev.filter((item) => item !== surface), surface]
                        : prev.filter((item) => item !== surface)
                    )
                  }
                />
                <Label
                  htmlFor={`${formId}-surface-${surface}`}
                  className='font-normal'
                >
                  {t(
                    surface === 'console'
                      ? 'qy_aa_surface_console'
                      : 'qy_aa_surface_picker'
                  )}
                </Label>
              </div>
            ))}
          </div>
          <p className='text-muted-foreground text-xs'>
            {surfaces.length === 0
              ? t('qy_aa_surfaces_required')
              : t('qy_aa_field_surfaces_hint')}
          </p>
        </div>

        <div className='space-y-1.5'>
          <Label htmlFor={`${formId}-color`}>{t('qy_aa_field_color')}</Label>
          <Select
            items={[
              {
                value: DEFAULT_COLOR_SENTINEL,
                label: (
                  <span className='flex items-center gap-2'>
                    <span
                      className={`size-4 rounded-full ${getBgColorClass('')}`}
                    />
                    {t('qy_aa_color_default')}
                  </span>
                ),
              },
              ...COLOR_VALUES.map((value) => ({
                value,
                label: (
                  <span className='flex items-center gap-2'>
                    <span
                      className={`size-4 rounded-full ${getBgColorClass(value)}`}
                    />
                    {value}
                  </span>
                ),
              })),
            ]}
            value={color === '' ? DEFAULT_COLOR_SENTINEL : color}
            onValueChange={(value: string | null) =>
              setColor(
                value == null || value === DEFAULT_COLOR_SENTINEL ? '' : value
              )
            }
          >
            <SelectTrigger id={`${formId}-color`}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectItem value={DEFAULT_COLOR_SENTINEL}>
                <span className='flex items-center gap-2'>
                  <span
                    className={`size-4 rounded-full ${getBgColorClass('')}`}
                  />
                  {t('qy_aa_color_default')}
                </span>
              </SelectItem>
              {COLOR_VALUES.map((value) => (
                <SelectItem key={value} value={value}>
                  <span className='flex items-center gap-2'>
                    <span
                      className={`size-4 rounded-full ${getBgColorClass(value)}`}
                    />
                    {value}
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className='text-muted-foreground text-xs'>
            {t('qy_aa_field_color_hint')}
          </p>
        </div>

        <div className='flex items-center justify-between gap-3 rounded-lg border p-3'>
          <div className='space-y-0.5'>
            <Label htmlFor={`${formId}-enabled`}>
              {t('qy_aa_field_enabled')}
            </Label>
            <p className='text-muted-foreground text-xs'>
              {t('qy_aa_field_enabled_hint')}
            </p>
          </div>
          <Switch
            id={`${formId}-enabled`}
            checked={enabled}
            onCheckedChange={setEnabled}
          />
        </div>
      </form>
    </QyResponsiveDialog>
  )
}
