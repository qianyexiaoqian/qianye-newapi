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
/**
 * 已选凭证的对外状态，在申请表单与上传控件之间传递。
 *
 * 合成一个对象而不是两个独立的布尔/字符串，是刻意的：`uploading` 与 `ref` 必须
 * 永远同时翻面（上传中一定还没有 ref，拿到 ref 就一定不在上传中）。拆成两个 prop
 * 之后，两次 setState 之间会存在一帧"既没在传、也没有 ref、但提交按钮已放行"的窗口。
 */
export type QyProofSelection = {
  /** 已上传成功、可用于 `proof_ref` 的标识；未选或失败时为 `null`。 */
  ref: string | null
  /** 上传在途。为真时**必须禁掉提交**，否则提交会带着一个还没拿到的 ref 走。 */
  uploading: boolean
}

/** 未选凭证。作为常量而不是四处写字面量，免得某一处漏改成"没在传但也没 ref"。 */
export const QY_PROOF_NONE: QyProofSelection = { ref: null, uploading: false }
