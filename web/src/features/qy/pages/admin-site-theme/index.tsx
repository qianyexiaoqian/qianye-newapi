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
import { Switch } from '@/components/ui/switch'
import { useThemeCustomization } from '@/context/theme-customization-provider'
import {
  DEFAULT_THEME_CUSTOMIZATION,
  THEME_PRESETS,
} from '@/lib/theme-customization'

import { QyConfirmDialog } from '../../components/qy-confirm-dialog'
import { QyPageBoundary } from '../../components/qy-page-boundary'
import { QySectionPageLayout } from '../../components/qy-section-page-layout'
import { qyErrorMessage } from '../../lib/api'
import { qyKeys } from '../../lib/query-keys'
import { qySaveSiteTheme, qySiteThemeQuery } from './api'
import { qyApplyPresetPreview } from './lib/preview'

/** 前端已知预设的展示信息。后端只给 value，名字与色卡在这里补。 */
const PRESET_META = new Map(THEME_PRESETS.map((p) => [p.value as string, p]))

/**
 * 站点默认主题设置页（超级管理员）。
 *
 * 这一页存在的理由：后端 `qianye/modules/sitetheme` 早就能存能审计，但没有任何
 * 界面调用它，运营只能 curl 或直接改库 —— 等于这条需求交付量为 0。
 *
 * 三个不能省的点：
 *  1. **下拉选项来自 GET 的 `allowed_presets`**。校验口径在后端，前端自己列一遍
 *     必然漂移成「能选、保存报错」。
 *  2. **强制模式必须显式警告**。它会忽略每一个访客自己选过的主题，是本页唯一
 *     具有全站破坏性的开关。
 *  3. **预览不落盘**。见 `lib/preview.ts`：只改 `<body>` 属性，离开即还原。
 */
export function QyAdminSiteTheme() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const query = useQuery(qySiteThemeQuery())
  const config = query.data
  const { customization } = useThemeCustomization()

  const [draftPreset, setDraftPreset] = useState('')
  const [draftForce, setDraftForce] = useState(false)
  const [confirmOpen, setConfirmOpen] = useState(false)

  // 服务端值到达（或被别的管理员改过后重新取到）时重置草稿：保留旧草稿会让人
  // 基于过期基线做修改，把别人刚改的值又覆盖回去。
  useEffect(() => {
    if (config == null) return
    setDraftPreset(config.default_preset)
    setDraftForce(config.force_preset)
  }, [config])

  const dirty =
    config != null &&
    (draftPreset !== config.default_preset ||
      draftForce !== config.force_preset)

  // 预览：草稿与服务端值不同时，把草稿预设挂到 <body>；否则还原成管理员自己的
  // 个人主题（也就是 ThemeCustomizationProvider 本来会设的值）。卸载时同样还原，
  // 否则这个属性会跟着管理员跑到别的页面上去。
  const personalPreset = customization.preset
  const previewPreset = dirty ? draftPreset : personalPreset
  useEffect(() => {
    if (typeof document === 'undefined') return
    const body = document.body
    if (body == null) return
    qyApplyPresetPreview(
      body,
      previewPreset,
      DEFAULT_THEME_CUSTOMIZATION.preset
    )
    return () => {
      qyApplyPresetPreview(
        body,
        personalPreset,
        DEFAULT_THEME_CUSTOMIZATION.preset
      )
    }
  }, [previewPreset, personalPreset])

  const saveMutation = useMutation({
    mutationFn: qySaveSiteTheme,
    onSuccess: async () => {
      setConfirmOpen(false)
      toast.success(t('qy_st_saved'))
      // 站点主题同时出现在管理端详情与引导端点 `/api/qy/config`（前端据此决定
      // 访客首屏主题）。只失效前者，本页会显示新值而全站仍按旧值渲染。
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: qyKeys.adminSiteTheme() }),
        queryClient.invalidateQueries({ queryKey: qyKeys.config() }),
      ])
    },
    onError: (error) => toast.error(qyErrorMessage(error, t)),
  })

  const unknownPreset =
    config != null &&
    draftPreset !== '' &&
    !config.allowed_presets.includes(draftPreset)

  return (
    <QySectionPageLayout>
      <QySectionPageLayout.Title>
        {t('qy_nav_a_site_theme')}
      </QySectionPageLayout.Title>
      <QySectionPageLayout.Content>
        <QyPageBoundary query={query}>
          {config != null && (
            <div className='grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] lg:items-start'>
              <Card data-card-hover='false'>
                <CardHeader>
                  <CardTitle>{t('qy_st_title')}</CardTitle>
                  <CardDescription>{t('qy_st_desc')}</CardDescription>
                </CardHeader>
                <CardContent className='space-y-4'>
                  <div className='space-y-1.5'>
                    <Label htmlFor='qy-st-select'>{t('qy_st_field')}</Label>
                    <Select
                      value={draftPreset}
                      onValueChange={(value) =>
                        setDraftPreset(value ?? config.default_preset)
                      }
                    >
                      <SelectTrigger id='qy-st-select' className='w-full'>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {config.allowed_presets.map((preset) => (
                          <SelectItem key={preset} value={preset}>
                            <PresetLabel
                              preset={preset}
                              upstreamDefault={config.upstream_default}
                              defaultSuffix={t('qy_st_upstream_default_tag')}
                            />
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                    <p className='text-muted-foreground text-xs'>
                      {t('qy_st_field_hint')}
                    </p>
                  </div>

                  {unknownPreset && (
                    <Alert variant='destructive'>
                      <TriangleAlert />
                      <AlertTitle>{t('qy_st_unknown_title')}</AlertTitle>
                      <AlertDescription>
                        {t('qy_st_unknown_desc', { preset: draftPreset })}
                      </AlertDescription>
                    </Alert>
                  )}

                  <div className='flex items-start justify-between gap-4'>
                    <div className='space-y-1'>
                      <Label htmlFor='qy-st-force'>{t('qy_st_force')}</Label>
                      <p className='text-muted-foreground text-xs'>
                        {t('qy_st_force_hint')}
                      </p>
                    </div>
                    <Switch
                      id='qy-st-force'
                      checked={draftForce}
                      onCheckedChange={setDraftForce}
                    />
                  </div>

                  {draftForce && (
                    <Alert variant='destructive'>
                      <TriangleAlert />
                      <AlertTitle>{t('qy_st_force_warn_title')}</AlertTitle>
                      <AlertDescription>
                        {t('qy_st_force_warn_desc')}
                      </AlertDescription>
                    </Alert>
                  )}

                  <Button
                    disabled={!dirty || saveMutation.isPending}
                    onClick={() => setConfirmOpen(true)}
                  >
                    {t('qy_st_save')}
                  </Button>
                </CardContent>
              </Card>

              <Card data-card-hover='false'>
                <CardHeader>
                  <CardTitle>{t('qy_st_preview_title')}</CardTitle>
                  <CardDescription>{t('qy_st_preview_desc')}</CardDescription>
                </CardHeader>
                <CardContent className='space-y-3'>
                  <Alert>
                    <Info />
                    <AlertTitle>{t('qy_st_preview_scope_title')}</AlertTitle>
                    <AlertDescription>
                      {t('qy_st_preview_scope_desc')}
                    </AlertDescription>
                  </Alert>
                  <dl className='divide-border divide-y text-sm'>
                    <StatusRow
                      label={t('qy_st_status_saved')}
                      value={presetName(config.default_preset)}
                    />
                    <StatusRow
                      label={t('qy_st_status_previewing')}
                      value={
                        dirty
                          ? presetName(draftPreset)
                          : t('qy_st_status_no_preview')
                      }
                    />
                    <StatusRow
                      label={t('qy_st_status_force')}
                      value={
                        config.force_preset ? t('qy_st_on') : t('qy_st_off')
                      }
                    />
                    <StatusRow
                      label={t('qy_st_status_upstream')}
                      value={presetName(config.upstream_default)}
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
        title={t('qy_st_confirm_title')}
        description={t('qy_st_confirm_desc')}
        confirmText={t('qy_st_save')}
        isLoading={saveMutation.isPending}
        details={
          <p className='text-sm'>
            <span className='text-muted-foreground'>
              {presetName(config?.default_preset ?? '')}
            </span>
            {' → '}
            <strong>{presetName(draftPreset)}</strong>
          </p>
        }
        onConfirm={() =>
          saveMutation.mutate({
            default_preset: draftPreset,
            force_preset: draftForce,
          })
        }
      />
    </QySectionPageLayout>
  )
}

/**
 * 预设的展示名。
 *
 * 后端可能下发一个前端这个版本还不认识的 value（两边版本不一致时）。那时原样
 * 显示 value 而不是空白 —— 运营至少能看出「有这么个东西，但我这版前端不认识」。
 * 预设名本身是专有名词（Steins Gate / Ocean Breeze），按 i18n 约定保留英文。
 */
function presetName(preset: string): string {
  return PRESET_META.get(preset)?.name ?? preset
}

function PresetLabel(props: {
  preset: string
  upstreamDefault: string
  defaultSuffix: string
}) {
  const meta = PRESET_META.get(props.preset)
  return (
    <span className='flex items-center gap-2'>
      {meta != null && (
        <span
          aria-hidden='true'
          className='border-border inline-block size-3 shrink-0 rounded-full border'
          style={{
            background: `linear-gradient(135deg, ${meta.swatches[0]}, ${meta.swatches[1]})`,
          }}
        />
      )}
      <span>{presetName(props.preset)}</span>
      {props.preset === props.upstreamDefault && (
        <span className='text-muted-foreground text-xs'>
          {props.defaultSuffix}
        </span>
      )}
    </span>
  )
}

function StatusRow(props: { label: string; value: string }) {
  return (
    <div className='flex items-center justify-between gap-3 py-1.5 first:pt-0 last:pb-0'>
      <dt className='text-muted-foreground'>{props.label}</dt>
      <dd className='font-medium'>{props.value}</dd>
    </div>
  )
}
