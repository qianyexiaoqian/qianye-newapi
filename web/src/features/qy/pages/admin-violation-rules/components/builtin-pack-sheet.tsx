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
import { useMutation, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'

import { QyResponsiveDialog } from '../../../components/qy-responsive-dialog'
import { qyKeys } from '../../../lib/query-keys'
import { qyOpsErrorMessage } from '../../ops/errors'
import {
  getQyViolationBuiltinCatalog,
  importQyViolationBuiltinRules,
} from '../api'
import type { QyViolationBuiltinItem, QyViolationBuiltinState } from '../types'

type QyBuiltinPackSheetProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onImported: () => void
}

/** 状态 → Badge 外观。`modified` 单独一档，它是唯一「点了升级也不会变」的状态。 */
const STATE_VARIANT: Record<
  QyViolationBuiltinState,
  'default' | 'outline' | 'secondary'
> = {
  not_imported: 'outline',
  up_to_date: 'secondary',
  upgradable: 'default',
  modified: 'default',
}

/**
 * 内置防护规则包。
 *
 * 项目方的要求是「他内置一些功能就能防护基本的:破限、逆向、蒸馏、高压」。
 * 这个抽屉就是那句话的入口：勾选、导入、结束。三条设计约束都写在界面上，
 * 因为它们决定了管理员点完之后会不会被吓一跳：
 *
 *  1. **导入出来一律是影子模式**（后端下发 `import_mode`，前端不写死）；
 *  2. **每条都带「防什么 / 典型误杀 / 来源 / 建议」**，误杀说明尤其重要 ——
 *     没有它，运营在收到第一个工单时无从判断该改窄还是该停用；
 *  3. **升级不覆盖改过的规则**：`modified` 那一档会被显式标出来并解释原因，
 *     否则「为什么点了升级它没变」是个只能读源码才能回答的问题。
 */
export function QyBuiltinPackSheet(props: QyBuiltinPackSheetProps) {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [upgrade, setUpgrade] = useState(false)

  const catalogQuery = useQuery({
    queryKey: qyKeys.adminViolationBuiltin(),
    queryFn: getQyViolationBuiltinCatalog,
    enabled: props.open,
    staleTime: 15_000,
  })

  const importMutation = useMutation({
    mutationFn: () =>
      importQyViolationBuiltinRules({
        keys: selected.size === 0 ? undefined : [...selected],
        upgrade,
      }),
    onSuccess: (result) => {
      // 逐条上报而不是一句"导入成功"：一次导入完全可能是「新建 3 条、跳过 9 条」，
      // 而被跳过的那些正是管理员最需要知道的（已存在 / 你改过 / 已是最新）。
      toast.success(t('qy_vio_builtin_import_done', { count: result.changed }))
      const skipped = result.results.filter((r) => r.action === 'skipped')
      for (const item of skipped.slice(0, 3)) {
        toast.info(`${item.key}: ${item.reason ?? ''}`)
      }
      void catalogQuery.refetch()
      props.onImported()
    },
    onError: (error) => toast.error(qyOpsErrorMessage(error, t)),
  })

  const items = catalogQuery.data?.items ?? []
  const categories = catalogQuery.data?.categories ?? []

  const toggle = (key: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  return (
    <QyResponsiveDialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('qy_vio_builtin_title')}
      description={t('qy_vio_builtin_desc')}
      contentClassName='sm:max-w-3xl'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => props.onOpenChange(false)}
          >
            {t('qy_common_cancel')}
          </Button>
          <Button
            type='button'
            disabled={importMutation.isPending || items.length === 0}
            onClick={() => importMutation.mutate()}
          >
            {selected.size === 0
              ? t('qy_vio_builtin_import_all')
              : t('qy_vio_builtin_import_selected', { count: selected.size })}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <Alert className='border-warning/40 bg-warning/5 [&>svg]:text-warning'>
          <AlertTitle>{t('qy_vio_builtin_shadow_title')}</AlertTitle>
          <AlertDescription>{t('qy_vio_builtin_shadow_desc')}</AlertDescription>
        </Alert>

        <div className='flex items-center gap-2'>
          <Checkbox
            id='qy-vio-builtin-upgrade'
            checked={upgrade}
            onCheckedChange={(v) => setUpgrade(v === true)}
          />
          <Label
            htmlFor='qy-vio-builtin-upgrade'
            className='text-sm font-normal'
          >
            {t('qy_vio_builtin_upgrade_label')}
          </Label>
        </div>

        {categories.map((category) => {
          const rows = items.filter((item) => item.category === category.id)
          if (rows.length === 0) return null
          return (
            <section key={category.id} className='space-y-2'>
              <div>
                <h3 className='text-sm font-medium'>{category.name_zh}</h3>
                <p className='text-muted-foreground text-xs'>{category.desc}</p>
              </div>
              <div className='space-y-2'>
                {rows.map((item) => (
                  <QyBuiltinRow
                    key={item.key}
                    item={item}
                    checked={selected.has(item.key)}
                    onToggle={() => toggle(item.key)}
                  />
                ))}
              </div>
            </section>
          )
        })}
      </div>
    </QyResponsiveDialog>
  )
}

function QyBuiltinRow(props: {
  item: QyViolationBuiltinItem
  checked: boolean
  onToggle: () => void
}) {
  const { t } = useTranslation()
  const item = props.item

  return (
    <div className='flex gap-3 rounded-lg border p-3'>
      <Checkbox
        className='mt-1'
        checked={props.checked}
        onCheckedChange={props.onToggle}
        aria-label={item.name}
      />
      <div className='min-w-0 flex-1 space-y-1'>
        <div className='flex flex-wrap items-center gap-2'>
          <span className='text-sm font-medium'>{item.name}</span>
          <Badge variant={STATE_VARIANT[item.state]}>
            {t(`qy_vio_builtin_state_${item.state}`)}
          </Badge>
          <Badge variant='outline'>
            {t(`qy_vio_match_${item.match_type}`)}
          </Badge>
          {item.state !== 'not_imported' && (
            <Badge variant='outline'>
              {t(
                `qy_vio_mode_${item.rule_mode === 'enforce' ? 'enforce' : 'shadow'}`
              )}
            </Badge>
          )}
        </div>
        <p className='text-muted-foreground text-xs'>{item.guards}</p>
        {/* 误杀说明用警示色：它不是补充信息，它是这条规则能不能转真实模式的
            全部依据。和「防什么」同样字号会让人一眼扫过去。 */}
        <p className='text-warning text-xs'>
          {t('qy_vio_builtin_false_positive', { text: item.false_positive })}
        </p>
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_builtin_advice', { text: item.advice })}
        </p>
        <p className='text-muted-foreground text-xs'>
          {t('qy_vio_builtin_origin', { text: item.origin })}
        </p>
        {item.state === 'modified' && (
          <p className='text-xs font-medium'>
            {t('qy_vio_builtin_modified_hint')}
          </p>
        )}
      </div>
    </div>
  )
}
