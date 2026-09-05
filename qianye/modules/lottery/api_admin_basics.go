package lottery

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_basics.go —— 发布之后改活动的标题与说明。
//
// 标题与说明**不进承诺原像**(commit.go 的 CommitHashV2 只有 act_no / kind / algo /
// rules_hash / spec_hash / 参与费 / 时刻 / draw_mode),它们与封面同一类:只决定用户
// 在卡片上看到什么字,不决定任何人能拿到几张票、开出什么结果。此前发布后没有任何
// 入口能改它们 —— 项目方 2026-09-05 原话「星屑转盘应当可以命名,当前不可以命名」。
//
// 对所有玩法开放(不只转盘):批次玩法的名字同样不在承诺里,一场叫「5」的活动发出去
// 之后只能整场取消重开,代价与收益完全不成比例。只在 settling / finished 之后关上:
// 那时证据链已经公示,改名会让公示页与用户记忆里的名字对不上。
//
// 每次改动写事件行 + 审计,before / after 各带标题与说明。

// basicsInput 是「改标题 / 说明」的请求体。
type basicsInput struct {
	Title string `json:"title"`
	Intro string `json:"intro"`
}

// ActionBasicsChanged 是改标题 / 说明的事件行 action。
const ActionBasicsChanged = "basics_changed"

// errBasicsLocked:活动已进入结算或已结束,名字随证据链一起公示过了。
var errBasicsLocked = newBizError(http.StatusConflict, "qy_lot_basics_locked",
	"已结算或已结束的活动不能再改名:证据链已经按这个名字公示")

// basicsEditableStatuses 是允许改名的状态:草稿、进行中、已封盘(等揭示)。
var basicsEditableStatuses = []string{StatusDraft, StatusPublished, StatusLocked}

func basicsSnapshot(title, intro string) map[string]any {
	return map[string]any{"title": title, "intro": intro}
}

// handleSetActivityBasics 改一场活动的标题与说明:
// PUT /admin/lottery/activities/:act_no/basics。校验与创建时同一口径。
func handleSetActivityBasics(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	const action = "lottery.activity.basics"
	actNo := c.Param("act_no")
	var in basicsInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, "请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	title := strings.TrimSpace(in.Title)
	var bad error
	switch {
	case title == "" || utf8.RuneCountInString(title) > 60:
		bad = errBadRequest("活动标题必填且不超过 60 个字")
	case utf8.RuneCountInString(in.Intro) > 2000:
		bad = errBadRequest("活动说明不超过 2000 个字")
	}
	if bad == nil {
		bad = rejectControlChars("活动标题", title)
	}
	if bad == nil {
		bad = rejectControlCharsAllowingBreaks("活动说明", in.Intro)
	}
	after := snapText(basicsSnapshot(title, in.Intro))
	if bad != nil {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(bad), "", after)
		respondErr(c, bad)
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
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(err), "", after)
		respondErr(c, err)
		return
	}
	before := snapText(basicsSnapshot(act.Title, act.Intro))
	if !statusIn(act.Status, basicsEditableStatuses) {
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(errBasicsLocked), before, after)
		respondErr(c, errBasicsLocked)
		return
	}

	now := common.GetTimestamp()
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// CAS 只认可改的三个状态:并发的揭示 / 收尾先落地,这次改名整笔不生效。
		res := tx.Model(&Activity{}).
			Where("id = ? AND status IN (?)", act.Id, basicsEditableStatuses).
			Updates(map[string]any{"title": title, "intro": in.Intro, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errBasicsLocked
		}
		return writeActivityEvent(tx, act.Id, act.Status, act.Status, ActionBasicsChanged,
			qymodel.ActorAdmin, c.GetInt("id"), map[string]any{
				"before": basicsSnapshot(act.Title, act.Intro),
				"after":  basicsSnapshot(title, in.Intro),
			})
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("更新活动标题", err)
		}
		writeAdminAudit(c, action, actNo, qymodel.ResultFail, auditReason(err), before, after)
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, action, actNo, qymodel.ResultOK, "", before, after)
	respondOK(c, gin.H{"act_no": actNo, "title": title, "intro": in.Intro})
}

// statusIn 报告 status 是否在集合里。
func statusIn(status string, set []string) bool {
	for _, s := range set {
		if s == status {
			return true
		}
	}
	return false
}
