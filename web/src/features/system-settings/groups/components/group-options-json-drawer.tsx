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
import { Code2 } from 'lucide-react'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  sideDrawerContentClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { JsonCodeEditor } from '@/components/json-code-editor'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'

export type GroupOptionJsonField = {
  /** 后端 option 键名，同时是本抽屉里的字段标识。 */
  key: string
  label: string
  /** 当前草稿序列化出来的 JSON。 */
  value: string
  /** 只读字段（这一页无权改它）。仍然显示 —— 排查时要看得到全貌。 */
  readOnly?: boolean
  description?: string
}

type GroupOptionsJsonDrawerProps = {
  fields: readonly GroupOptionJsonField[]
  /** 应用回表格。只回传可写字段，键与 `field.key` 一致。 */
  onApply: (next: Record<string, string>) => void
}

/**
 * 「高级 / JSON」逃生抽屉。
 *
 * ── 它编辑的是**当前草稿**，不是另一份状态 ──
 *
 * 上游那一页的做法是可视化编辑器与 JSON 编辑器**并排绑同一组表单字段**，
 * 两者随时同步。拆页之后每一页的权威状态是结构化的行（行有身份、有中间态、
 * 有「不在倍率表里」这种 null），再让一个 JSON 编辑器双向绑住同一份数据，
 * 就要在每一次按键上做一轮「序列化 → 反序列化 → 重建行身份」，输入框会在
 * 敲小数点的时候跳。
 *
 * 所以这里是**导入 / 导出**语义：打开时把当前草稿序列化给你看，按「应用」
 * 才把它解析回表格。抽屉关掉不影响草稿，应用之后仍然要按页面上的保存 ——
 * 逃生口不该顺带获得一条绕过保存闸门的写入路径。
 *
 * 语法不合法时「应用」是禁用的：把一段坏 JSON 灌回表格，表格会退化成空行，
 * 而那看起来和「这个站点没有配过任何分组」一模一样。
 */
export function GroupOptionsJsonDrawer(props: GroupOptionsJsonDrawerProps) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<Record<string, string>>({})

  /*
    每次打开都从当前草稿重新取值：抽屉是当前状态的一个视图，
    留着上一次打开时的内容会让人应用一份早已过时的 JSON。

    ── 依赖只有 `open`，**刻意不含 `props.fields`** ──

    三个调用点传的都是行内数组字面量，每一次父级渲染都是新引用。把它列进依赖，
    抽屉开着期间父组件重渲染一次（首屏那两个 `retry:false` 的查询落地就会触发）
    就把管理员刚粘进 JSON 编辑器的内容静默还原成打开那一刻的值，没有任何提示 ——
    运营会以为自己没粘上，再粘一次。抽屉打开那一刻取一次快照才是这里要的语义。
  */
  const fieldsRef = useRef(props.fields)
  fieldsRef.current = props.fields
  useEffect(() => {
    if (!open) return
    const next: Record<string, string> = {}
    for (const field of fieldsRef.current) next[field.key] = field.value
    setDraft(next)
  }, [open])

  const invalidKeys = useMemo(() => {
    const invalid: string[] = []
    for (const field of props.fields) {
      if (field.readOnly) continue
      const raw = draft[field.key]
      if (raw === undefined) continue
      try {
        JSON.parse(raw)
      } catch {
        invalid.push(field.key)
      }
    }
    return invalid
  }, [draft, props.fields])

  const { onApply } = props
  const handleApply = useCallback(() => {
    const next: Record<string, string> = {}
    for (const field of props.fields) {
      if (field.readOnly) continue
      const raw = draft[field.key]
      if (raw !== undefined) next[field.key] = raw
    }
    onApply(next)
    setOpen(false)
  }, [draft, onApply, props.fields])

  return (
    <>
      <Button variant='outline' size='sm' onClick={() => setOpen(true)}>
        <Code2 className='mr-2 h-4 w-4' />
        {t('qy_gs_json_escape')}
      </Button>

      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent
          side='right'
          className={sideDrawerContentClassName('sm:max-w-2xl')}
        >
          <SheetHeader className={sideDrawerHeaderClassName()}>
            <SheetTitle>{t('qy_gs_json_escape')}</SheetTitle>
            <SheetDescription>{t('qy_gs_json_escape_desc')}</SheetDescription>
          </SheetHeader>

          <div className={sideDrawerFormClassName('gap-4')}>
            {props.fields.map((field) => (
              <div key={field.key} className='space-y-1.5'>
                <div className='flex items-baseline justify-between gap-2'>
                  <span className='text-sm font-medium'>{field.label}</span>
                  <code className='text-muted-foreground text-xs'>
                    {field.key}
                    {field.readOnly ? ` · ${t('qy_gs_read_only')}` : ''}
                  </code>
                </div>
                <JsonCodeEditor
                  value={draft[field.key] ?? field.value}
                  onChange={(value) =>
                    setDraft((current) => ({ ...current, [field.key]: value }))
                  }
                  name={field.key}
                  disabled={field.readOnly}
                  heightClassName='h-40 min-h-40 max-h-40'
                />
                {field.description && (
                  <p className='text-muted-foreground text-xs leading-5'>
                    {field.description}
                  </p>
                )}
              </div>
            ))}

            {invalidKeys.length > 0 && (
              <Alert variant='destructive'>
                <AlertDescription>
                  {t('qy_gs_json_invalid', { keys: invalidKeys.join(', ') })}
                </AlertDescription>
              </Alert>
            )}

            <div className='flex justify-end gap-2'>
              <Button
                variant='outline'
                size='sm'
                onClick={() => setOpen(false)}
              >
                {t('Cancel')}
              </Button>
              <Button
                size='sm'
                disabled={invalidKeys.length > 0}
                onClick={handleApply}
              >
                {t('qy_gs_json_apply')}
              </Button>
            </div>
          </div>
        </SheetContent>
      </Sheet>
    </>
  )
}
