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
package lottery

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_picks.go —— 「一次最多下多少注」这一格的校验、报错与发布后改值。
//
// # 为什么它可以在发布之后改,而每人上限不行
//
// 承诺哈希冻结的是**会改变结果的东西**:参与条件、奖档、四个时刻、算法版本、
// 号池、种子。每人参与上限在里面,因为它决定每个人最多能拿到几张票,而票数
// 直接就是中奖概率。
//
// 这一格决定的只是"同样这些票要分几次请求买完"。上限 10 与上限 999 之下,
// 一个用户最终能持有的票数完全相同(那个数由 max_entries_per_user 说了算),
// 每一张票的号码、序号、链环、资金单也逐字节相同 —— 它对参与者不构成任何一项
// 承诺,只是一条吞吐旋钮。把它塞进 commit_hash 的代价是实打实的:一场正在进行
// 的活动里运营发现 10 注太少,唯一的补救会变成"取消这一期、全额退款、重开一期"。
//
// 所以它与封面同一类:不进任何哈希原像 → 发布后可写 → 每一次改动写审计。
// 奖档、时刻、参与条件、号池永远不在此列。

// checkPicksPerRequest 校验运营填进这一格的数。
//
// 0 是合法值,含义是"没配过,按默认走"(见 Activity.MaxPicksPerRequest 的零值
// 口径)。负数与超过硬顶的一律拒绝,而且报错里必须带上那两个数 —— 一句
// "取值不合法"会让运营去二分试,而这一格的合法区间是死的。
func checkPicksPerRequest(n int) error {
	if n < 0 || n > maxPicksPerRequestHard {
		return errBadRequest(fmt.Sprintf(
			"「一次最多下多少注」请填 0 到 %d 之间的整数(填 0 = 不配置,按默认 %d 注)—— "+
				"上限 %d 是这条链路的物理量级:一次 N 注在服务端是 N 次串行扣费,"+
				"每一注一张独立资金单、一条链环、一份可复算回执,满配实测约 %d 秒",
			maxPicksPerRequestHard, defaultPicksPerRequest, maxPicksPerRequestHard,
			(maxPicksPerRequestHard*measuredMsPerPick+999)/1000))
	}
	return nil
}

// tooManyPicks 是"一次买太多注"。
//
// 文案里必须带上**这一场**的那个数:改造前它是一个包级 var,里面写死着 10,
// 而这一格现在是每场各配的 —— 一条恒说 10 的报错在配了 200 的活动上就是假话。
func tooManyPicks(cap int) *bizError {
	return newBizError(http.StatusBadRequest, "qy_lot_too_many_picks",
		fmt.Sprintf("一次最多买 %d 注,请分几次提交", cap))
}

// picksCapInput 是「改这一场的单次注数上限」的请求体。
type picksCapInput struct {
	// MaxPicksPerRequest 与创建时同一个字段名、同一套零值口径:
	// 0 = 不配置,按默认走。
	MaxPicksPerRequest int `json:"max_picks_per_request"`
}

// handleSetPicksCap 改一场活动的单次注数上限。**不限状态。**
//
// 与换封面同姿势(包括那条"值没变就直接回 200"的幂等收口):CAS 的 WHERE 与 SET
// 写的是同一列,一次完全相同的重发在 MySQL 下一列都没改到 → RowsAffected == 0,
// 而根本没有第二个人改过。判等放在 CAS 之外,CAS 本身一个字都不松 —— 它挡的是
// 两个管理员同时改这一格时后到的那次把先到的悄悄覆盖掉。
func handleSetPicksCap(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	actNo := c.Param("act_no")
	var in picksCapInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "lottery.activity.picks_cap", actNo, qymodel.ResultFail,
			"请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	if err := checkPicksPerRequest(in.MaxPicksPerRequest); err != nil {
		writeAdminAudit(c, "lottery.activity.picks_cap", actNo, qymodel.ResultFail,
			auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}
	act, err := loadActivityAny(ctx, gdb, actNo)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.picks_cap", actNo, qymodel.ResultFail,
			auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	before := snapText(map[string]any{
		"max_picks_per_request": act.MaxPicksPerRequest,
		"effective":             picksCapOf(act),
	})
	// 快照里同时记原始值与生效值:0 与 10 在行为上一模一样,只看原始值的话
	// 事后分不出"运营把它清空了"与"运营明确填了 10"。
	after := snapText(map[string]any{
		"max_picks_per_request": in.MaxPicksPerRequest,
		"effective":             picksCapOf(&Activity{MaxPicksPerRequest: in.MaxPicksPerRequest}),
	})

	if act.MaxPicksPerRequest == in.MaxPicksPerRequest {
		writeAdminAudit(c, "lottery.activity.picks_cap", actNo, qymodel.ResultOK,
			"单次注数上限未变化", before, after)
		respondOK(c, gin.H{"act_no": actNo,
			"max_picks_per_request": in.MaxPicksPerRequest,
			"effective":             picksCapOf(act)})
		return
	}

	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&Activity{}).
			Where("id = ? AND max_picks_per_request = ?", act.Id, act.MaxPicksPerRequest).
			Updates(map[string]any{
				"max_picks_per_request": in.MaxPicksPerRequest,
				"updated_at":            common.GetTimestamp(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errPicksCapRaced
		}
		return nil
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("更新单次注数上限", err)
		}
		writeAdminAudit(c, "lottery.activity.picks_cap", actNo, qymodel.ResultFail,
			auditReason(err), before, "")
		respondErr(c, err)
		return
	}

	writeAdminAudit(c, "lottery.activity.picks_cap", actNo, qymodel.ResultOK, "", before, after)
	respondOK(c, gin.H{"act_no": actNo,
		"max_picks_per_request": in.MaxPicksPerRequest,
		"effective":             picksCapOf(&Activity{MaxPicksPerRequest: in.MaxPicksPerRequest})})
}

// errPicksCapRaced 与 errCoverRaced 同一个形状:活动状态没问题,
// 只是你读到的那个值已经不是现在这一份。
var errPicksCapRaced = newBizError(http.StatusConflict, "qy_lot_picks_cap_raced",
	"该活动的单次注数上限刚刚被改过,请刷新后重试")
