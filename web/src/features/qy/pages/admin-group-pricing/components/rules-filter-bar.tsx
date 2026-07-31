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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

import { QyFilterBar, QyFilterField } from '../../ops/qy-ops-ui'
import { QY_GP_MODES } from '../lib/rule-form'

/** 「全部」在 Select 里的哨兵值。空串在 Base UI 里会被当成未选中。 */
const ALL = '__qy_gp_all__'

type QyGpRulesFilterBarProps = {
  groups: string[]
  group: string
  onGroupChange: (group: string) => void
  model: string
  onModelChange: (model: string) => void
  mode: string
  onModeChange: (mode: string) => void
}

/**
 * 规则列表的筛选栏。
 *
 * 后端按 `group_name` 精确、`model_name` 模糊、`mode` 精确三种方式过滤，
 * 这里就照这三种给控件 —— 模型是模糊匹配，所以给自由输入而不是下拉，
 * 否则查不到 `gpt-4*` 这类通配规则。
 */
export function QyGpRulesFilterBar(props: QyGpRulesFilterBarProps) {
  const { t } = useTranslation()
  const groupId = useId()
  const modelId = useId()
  const modeId = useId()

  return (
    <QyFilterBar>
      <QyFilterField label={t('qy_gp_col_group')} htmlFor={groupId}>
        <Select
          value={props.group === '' ? ALL : props.group}
          onValueChange={(value) =>
            props.onGroupChange(value == null || value === ALL ? '' : value)
          }
        >
          <SelectTrigger id={groupId} className='w-40'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
            {props.groups.map((group) => (
              <SelectItem key={group} value={group}>
                {group}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </QyFilterField>

      <QyFilterField label={t('qy_gp_col_model')} htmlFor={modelId}>
        <Input
          id={modelId}
          value={props.model}
          onChange={(event) => props.onModelChange(event.target.value)}
          placeholder={t('qy_gp_filter_model_ph')}
          className='w-52'
        />
      </QyFilterField>

      <QyFilterField label={t('qy_gp_col_mode')} htmlFor={modeId}>
        <Select
          value={props.mode === '' ? ALL : props.mode}
          onValueChange={(value) =>
            props.onModeChange(value == null || value === ALL ? '' : value)
          }
        >
          <SelectTrigger id={modeId} className='w-36'>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('qy_common_all')}</SelectItem>
            {QY_GP_MODES.map((mode) => (
              <SelectItem key={mode} value={mode}>
                {t(`qy_gp_mode_${mode}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </QyFilterField>
    </QyFilterBar>
  )
}
