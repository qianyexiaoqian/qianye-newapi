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
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import type { QyLotRules } from '../../lottery/types'
import type { QyLotDraft } from '../lib/draft'

/**
 * 参与条件编辑（向导第三步）。
 *
 * ## 分成两组不是排版偏好
 *
 * 上半是**用户属性**条件（分组、余额、消费、风控、账号年龄），下半是**频次与
 * 去重**上限。两者最终都会被规范化进同一份 `rules_text` → `rules_hash` →
 * `commit_hash`：频次上限同样是参与条件，不进承诺就意味着事后改上限不会被任何
 * 验证检出。活动行上那几列只是给锁内判定用的物化副本。
 *
 * 分成两组纯粹是因为运营的心智模型是分开的（"谁能参加" vs "能参加几次"）。
 *
 * ## 每一条的代价必须写在界面上，不是文档里
 *
 * 「IP 去重会误伤家庭 / 公司共用出口的真实用户」这句话写在设计文档里没有意义：
 * 打开这个开关的人不会去读文档。所以它就印在开关旁边。
 */
export function QyLotRulesEditor(props: {
  draft: QyLotDraft
  onChange: (patch: Partial<QyLotDraft>) => void
}) {
  const { draft } = props
  const { t } = useTranslation()

  const patchRules = (patch: Partial<QyLotRules>) => {
    props.onChange({ rules: { ...draft.rules, ...patch } })
  }

  return (
    <div className='space-y-5'>
      <section className='space-y-3'>
        <h4 className='text-sm font-medium'>{t('qy_lot_rules_who_title')}</h4>

        <ListField
          label={t('qy_lot_rule_f_group_allow')}
          hint={t('qy_lot_rule_f_group_allow_hint')}
          value={draft.rules.allow_groups}
          onChange={(value) => patchRules({ allow_groups: value })}
        />
        <ListField
          label={t('qy_lot_rule_f_group_deny')}
          hint={t('qy_lot_rule_f_group_deny_hint')}
          value={draft.rules.deny_groups}
          onChange={(value) => patchRules({ deny_groups: value })}
        />

        <NumberField
          label={t('qy_lot_rule_f_account_age')}
          hint={t('qy_lot_rule_f_account_age_hint')}
          value={draft.rules.min_account_age_days}
          onChange={(value) => patchRules({ min_account_age_days: value })}
        />
        <NumberField
          label={t('qy_lot_rule_f_min_quota')}
          hint={t('qy_lot_rule_f_min_quota_hint')}
          value={draft.rules.min_quota}
          onChange={(value) => patchRules({ min_quota: value })}
        />
        <NumberField
          label={t('qy_lot_rule_f_used_quota')}
          hint={t('qy_lot_rule_f_used_quota_hint')}
          value={draft.rules.min_used_quota}
          onChange={(value) => patchRules({ min_used_quota: value })}
        />
        <div className='grid gap-3 sm:grid-cols-2'>
          <NumberField
            label={t('qy_lot_rule_f_spend_days')}
            hint={t('qy_lot_rule_f_spend_days_hint')}
            value={draft.rules.recent_spend_days}
            onChange={(value) => patchRules({ recent_spend_days: value })}
          />
          <NumberField
            label={t('qy_lot_rule_f_spend_quota')}
            hint={t('qy_lot_rule_f_spend_quota_hint')}
            value={draft.rules.recent_spend_quota}
            onChange={(value) => patchRules({ recent_spend_quota: value })}
          />
        </div>
      </section>

      <section className='space-y-3'>
        <h4 className='text-sm font-medium'>{t('qy_lot_rules_risk_title')}</h4>

        <SwitchField
          label={t('qy_lot_rule_f_violation')}
          hint={t('qy_lot_rule_f_violation_hint')}
          checked={draft.rules.exclude_violation}
          onChange={(checked) => patchRules({ exclude_violation: checked })}
        />
        {/* 「有没有」与「最近多闹腾」是两条独立的判据，运营可能只想挡后者。 */}
        <NumberField
          label={t('qy_lot_rule_f_violation_hits')}
          hint={t('qy_lot_rule_f_violation_hits_hint')}
          value={draft.rules.max_violation_hits}
          onChange={(value) => patchRules({ max_violation_hits: value })}
        />
        {/* 两项刻意分开：管理员**手工**封禁不写 `qy_violation_ban`，只改
            `users.status`。合成一项会让运营以为自己拦住了实际没拦住的人。 */}
        <SwitchField
          label={t('qy_lot_rule_f_ever_banned')}
          hint={t('qy_lot_rule_f_ever_banned_hint')}
          checked={draft.rules.exclude_ever_auto_banned}
          onChange={(checked) =>
            patchRules({ exclude_ever_auto_banned: checked })
          }
        />
        <SwitchField
          label={t('qy_lot_rule_f_disabled')}
          hint={t('qy_lot_rule_f_disabled_hint')}
          checked={draft.rules.exclude_currently_disabled}
          onChange={(checked) =>
            patchRules({ exclude_currently_disabled: checked })
          }
        />

        {/* 本系统没有实名信息，这句话必须直接写出来而不是含糊其辞：
            运营以为自己开了"仅限实名用户"，实际开的是"绑过邮箱的用户"。 */}
        <p className='text-muted-foreground rounded-lg border p-3 text-xs'>
          {t('qy_lot_rule_no_kyc_note')}
        </p>
        <SwitchField
          label={t('qy_lot_rule_f_email')}
          checked={draft.rules.require_email}
          onChange={(checked) => patchRules({ require_email: checked })}
        />
        <SwitchField
          label={t('qy_lot_rule_f_oauth')}
          checked={draft.rules.require_oauth}
          onChange={(checked) => patchRules({ require_oauth: checked })}
        />
        <SwitchField
          label={t('qy_lot_rule_f_pay_password')}
          checked={draft.rules.require_pay_password}
          onChange={(checked) => patchRules({ require_pay_password: checked })}
        />
      </section>

      <section className='space-y-3'>
        <h4 className='text-sm font-medium'>{t('qy_lot_rules_freq_title')}</h4>

        <div className='grid gap-3 sm:grid-cols-2'>
          <NumberField
            label={t('qy_lot_rule_f_max_entries')}
            hint={t('qy_lot_rule_f_max_entries_hint')}
            value={draft.max_entries_per_user}
            onChange={(value) =>
              props.onChange({ max_entries_per_user: value })
            }
          />
          <NumberField
            label={t('qy_lot_rule_f_max_attempts')}
            hint={t('qy_lot_rule_f_max_attempts_hint')}
            value={draft.max_attempts_per_user}
            onChange={(value) =>
              props.onChange({ max_attempts_per_user: value })
            }
          />
          {/* 两个上限分开：允许多次参与时，人数上限拦不住一个人刷 1000 笔。 */}
          <NumberField
            label={t('qy_lot_rule_f_max_total_entries')}
            hint={t('qy_lot_rule_f_max_total_entries_hint')}
            value={draft.max_total_entries}
            onChange={(value) => props.onChange({ max_total_entries: value })}
          />
          <NumberField
            label={t('qy_lot_rule_f_max_total_users')}
            hint={t('qy_lot_rule_f_max_total_users_hint')}
            value={draft.max_total_users}
            onChange={(value) => props.onChange({ max_total_users: value })}
          />
          <NumberField
            label={t('qy_lot_rule_f_cooldown')}
            hint={t('qy_lot_rule_f_cooldown_hint')}
            value={draft.cooldown_seconds}
            onChange={(value) => props.onChange({ cooldown_seconds: value })}
          />
          <NumberField
            label={t('qy_lot_rule_f_per_inviter')}
            hint={t('qy_lot_rule_f_per_inviter_hint')}
            value={draft.rules.max_per_inviter}
            onChange={(value) => patchRules({ max_per_inviter: value })}
          />
        </div>

        <SwitchField
          label={t('qy_lot_rule_f_dedup_ip')}
          hint={t('qy_lot_rule_f_dedup_ip_hint')}
          checked={draft.dedup_ip}
          onChange={(checked) => props.onChange({ dedup_ip: checked })}
        />
      </section>
    </div>
  )
}

function NumberField(props: {
  label: string
  hint?: string
  value: number
  onChange: (value: number) => void
}) {
  const id = useId()
  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{props.label}</Label>
      <Input
        id={id}
        inputMode='numeric'
        value={String(props.value)}
        onChange={(event) => {
          // 只收非负整数。空串归 0（= 不限），这是全表统一的口径。
          const digits = event.target.value.replaceAll(/\D/g, '')
          props.onChange(digits === '' ? 0 : Number(digits))
        }}
      />
      {props.hint != null && (
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      )}
    </div>
  )
}

function SwitchField(props: {
  label: string
  hint?: string
  checked: boolean
  onChange: (checked: boolean) => void
}) {
  const id = useId()
  return (
    <div className='flex items-start justify-between gap-4 rounded-lg border p-3'>
      <div className='min-w-0'>
        <Label htmlFor={id}>{props.label}</Label>
        {props.hint != null && (
          <p className='text-muted-foreground text-xs'>{props.hint}</p>
        )}
      </div>
      <Switch
        id={id}
        checked={props.checked}
        onCheckedChange={props.onChange}
      />
    </div>
  )
}

/**
 * 分组名清单。逗号分隔的单行输入 —— 分组名是站点自定义的短标识，
 * 给它做一个带搜索的多选控件不值当，而逗号分隔在这里不存在歧义
 * （分组名里不允许出现逗号）。
 */
function ListField(props: {
  label: string
  hint?: string
  value: string[]
  onChange: (value: string[]) => void
}) {
  const id = useId()
  return (
    <div className='space-y-1'>
      <Label htmlFor={id}>{props.label}</Label>
      <Input
        id={id}
        value={props.value.join(',')}
        onChange={(event) => {
          props.onChange(
            event.target.value
              .split(',')
              .map((item) => item.trim())
              .filter((item) => item !== '')
          )
        }}
      />
      {props.hint != null && (
        <p className='text-muted-foreground text-xs'>{props.hint}</p>
      )}
    </div>
  )
}
