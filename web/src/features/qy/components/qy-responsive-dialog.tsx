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
import type { ReactNode } from 'react'

import { Dialog } from '@/components/dialog'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { useIsMobile } from '@/hooks/use-mobile'
import { cn } from '@/lib/utils'

export type QyResponsiveDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: ReactNode
  description?: ReactNode
  /**
   * 底部操作区。桌面端落在 `DialogFooter`、移动端落在 `SheetFooter`，
   * 两边都**不随正文滚动**。
   *
   * 表单的提交按钮要放在这里而不是正文末尾：正文是滚动区，按钮跟着滚出屏幕
   * 之后，用户在长表单里找不到"保存"。用 `<Button form={FORM_ID} type='submit'>`
   * 跨出 `<form>` 提交（HTML 原生的 form 属性），不需要把 form 包到外面。
   */
  footer?: ReactNode
  children: ReactNode
  /** 桌面端窗口宽度上限，例如 `sm:max-w-xl`。缺省用上游 Dialog 的 `sm:max-w-2xl`。 */
  contentClassName?: string
  /** 移动端抽屉宽度上限，例如 `sm:max-w-md`。缺省用上游 Sheet 的 `sm:max-w-sm`。 */
  sheetClassName?: string
}

/**
 * qy 所有"内容/表单浮层"的统一外壳（需求 1）。
 *
 * ## 项目方要的口径
 *
 * 原话：「当前的弹窗你的方案是从底部伸出来，缺失了很多信息，你需要让他居中
 * 显示出一个完整的窗口，展示完整的内容，对于手机端的原项目从左边伸出这不挺好
 * 的？」拆成两条可执行的规则：
 *
 *   · **桌面端 = 居中的完整窗口**。宽度按内容给（默认 `sm:max-w-2xl`），
 *     高度顶到 `100vh - 2rem`，正文自己滚。不是贴边的窄条。
 *   · **移动端 = 从侧边伸出的抽屉**，与上游移动端侧栏同一方向（左）。
 *     窄屏上居中窗口两侧的留白是浪费，而侧边抽屉是这个项目里用户已经熟悉的
 *     交互 —— 项目方点名说那样"挺好"。
 *
 * ## 为什么是一个外壳而不是每个页面自己判
 *
 * 改造前 qy 有两种浮层各自为政：联系人簿走居中 `Dialog`，四张管理端表单走
 * `Sheet side='right'`（桌面上是一条 `max-w-xl` 的贴边窄板）。同一个产品里
 * "打开一个表单"出现两种形态，正是本仓反复出现的「同一概念的第 N 份拷贝」。
 * 收成一个外壳之后，"桌面居中 / 移动侧出"这条规则只有一处实现，
 * `__tests__/qy-responsive-dialog.test.ts` 反向扫描 `features/qy` 下不再有
 * 第二处直接用 `SheetContent` 的地方。
 *
 * ## 断点取上游的 `useIsMobile`（768px）
 *
 * 不自己写 `matchMedia`：上游侧栏、日志筛选抽屉用的都是它，多一份阈值就会出现
 * "侧栏已经收起了、弹窗还当自己在桌面"的错位。它在首帧返回 `false`
 * （`useState<boolean|undefined>` → `!!undefined`），也就是说 SSR/首屏那一帧
 * 按桌面渲染。对本组件无影响：浮层只在用户点击后才 `open`，那时 effect 早已跑过。
 */
export function QyResponsiveDialog(props: QyResponsiveDialogProps) {
  const isMobile = useIsMobile()

  if (isMobile) {
    return (
      <Sheet open={props.open} onOpenChange={props.onOpenChange}>
        <SheetContent side='left' className={props.sheetClassName}>
          <SheetHeader>
            {/* pr-8 给右上角的关闭按钮让位，标题不会压在它下面。 */}
            <SheetTitle className='pr-8'>{props.title}</SheetTitle>
            {props.description ? (
              <SheetDescription>{props.description}</SheetDescription>
            ) : null}
          </SheetHeader>
          <div className='min-h-0 flex-1 overflow-x-hidden overflow-y-auto overscroll-contain px-4'>
            {props.children}
          </div>
          {props.footer ? (
            <SheetFooter className='flex-row justify-end gap-2 border-t'>
              {props.footer}
            </SheetFooter>
          ) : null}
        </SheetContent>
      </Sheet>
    )
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={props.title}
      description={props.description}
      contentClassName={cn('sm:max-w-2xl', props.contentClassName)}
      footer={props.footer}
    >
      {props.children}
    </Dialog>
  )
}
