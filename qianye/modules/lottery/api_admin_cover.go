package lottery

import (
	"context"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin_cover.go —— 封面的三个写接口与一个匿名读接口。
//
// # 为什么取回封面是**匿名**的
//
// `<img src="...">` 这条请求由浏览器自己发出,它**不带 Authorization 头**。
// 工单截图那边因此不得不走 axios 取 Blob 再 createObjectURL —— 那对一张详情页
// 里的几张图是合理的代价,但大厅首屏是十几张卡片并排,每张都要一次 XHR、
// 一个 blob URL 与一次撤销,而且 blob 拿不到任何 HTTP 缓存。
//
// 匿名是安全的,前提有三条,缺一条都不成立:
//
//  1. **只回已经落到某场活动上的封面**。未绑定的上传只属于上传者本人,
//     匿名可取等于把管理员挑到一半的图公开出去。
//  2. ref 是 128 位随机数(crypto/rand,见 common.GetUUID),不可枚举。
//  3. 封面本来就是**面向所有访客的展示物**:它与活动标题一样,是给人看的招贴,
//     不含任何一个用户的私人信息。这与工单截图(可能是身份材料)、
//     提现凭证(含卡号姓名)是完全不同的东西 —— 后两者永远不会走这条路。
//
// 上传与删除仍然只在管理端(AdminAuth):普通用户没有任何一条能往本站磁盘上
// 写字节的封面路径。

// handleUploadCover 接收一张封面,返回可以填进活动的 ref。已挂 CriticalRateLimit。
func handleUploadCover(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	adminId := c.GetInt("id")
	row, err := acceptCoverUpload(c, adminId)
	if err != nil {
		// 失败的上传没有产生任何存储副作用,它的调用事实由请求台账
		// (qy_request_audits)覆盖,不再单写一条业务审计。
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "lottery.cover.upload", row.Ref, qymodel.ResultOK, "", "",
		snapText(map[string]any{
			"ref": row.Ref, "mime_type": row.MimeType, "size": row.Size,
		}))
	respondOK(c, gin.H{
		"ref":        row.Ref,
		"mime_type":  row.MimeType,
		"size":       row.Size,
		"created_at": row.CreatedAt,
	})
}

// handleDiscardCover 丢弃一张【自己上传且尚未用在任何活动上】的封面。
//
// 与上传那条成对:一张图从磁盘上出现与消失都要能对上,否则"这个账号传了多少、
// 现在还剩多少"在事后只剩一半答案。
func handleDiscardCover(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		respondErr(c, errCoverNotFound)
		return
	}
	if err := discardPendingCover(c.GetInt("id"), ref); err != nil {
		respondErr(c, err)
		return
	}
	writeAdminAudit(c, "lottery.cover.discard", ref, qymodel.ResultOK, "",
		snapText(map[string]any{"ref": ref}), "")
	respondOK(c, gin.H{"ref": ref})
}

// coverInput 是"把这场活动的封面换成 X"的请求体。
//
// 两个字段都为空 = 清空封面。这是刻意的:没有单独的「删除封面」接口 ——
// 一个动作两个入口迟早会在其中一个上漏掉解绑那一步,而漏掉的后果是一张
// 永远没人回收的磁盘文件。
type coverInput struct {
	CoverUrl string `json:"cover_url"`
	CoverRef string `json:"cover_ref"`
}

// resolveCoverInput 把请求体归一成"最终要写进活动行的那两列"。
//
// 互斥判定在这里做一次,创建/修改/换图三条路共用它 —— 三处各写一遍的话,
// 漏掉的那一处会让一场活动同时挂着外链与上传图,而显示哪一张只能由前端的
// 判断顺序回答。
func resolveCoverInput(in coverInput) (coverURL, coverRef string, err error) {
	url, err := normalizeCoverURL(in.CoverUrl)
	if err != nil {
		return "", "", err
	}
	ref := in.CoverRef
	if url != "" && ref != "" {
		return "", "", errCoverBothSources
	}
	return url, ref, nil
}

// handleSetActivityCover 换掉一场活动的封面。**不限状态。**
//
// 这是本模块里极少数在 publish 之后仍然可写的东西,理由只有一条:
// 封面不进 commit_hash / rules_hash / spec_hash 的任何一个原像。它不参与
// 结果的推导,也不是对用户的任何一项承诺 —— 而一个 404 的外链封面出现在
// 一场正在进行的活动上时,必须有办法修好它。奖档、时刻、参与条件永远不在此列。
//
// 换下来的旧图只打 detached_at,由回收任务在宽限期之后删文件:磁盘操作不参与
// 事务回滚,当场删掉而事务随后回滚,活动行上就会指着一个已经不存在的文件。
func handleSetActivityCover(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagLottery) {
		return
	}
	actNo := c.Param("act_no")
	var in coverInput
	if err := c.ShouldBindJSON(&in); err != nil {
		writeAdminAudit(c, "lottery.activity.cover", actNo, qymodel.ResultFail,
			"请求体解析失败", "", "")
		respondErr(c, errBadRequest("请求参数不合法"))
		return
	}
	coverURL, coverRef, err := resolveCoverInput(in)
	if err != nil {
		writeAdminAudit(c, "lottery.activity.cover", actNo, qymodel.ResultFail,
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
		writeAdminAudit(c, "lottery.activity.cover", actNo, qymodel.ResultFail,
			auditReason(err), "", "")
		respondErr(c, err)
		return
	}

	before := snapText(map[string]any{"cover_url": act.CoverUrl, "cover_ref": act.CoverRef})
	adminId := c.GetInt("id")

	// 幂等重发在这里就地收口,不进事务。
	//
	// 下面那条 CAS 的 WHERE 与 SET 写的是同三列,一次**完全相同**的重发
	// (前端双击、客户端重试、网关重投)在 MySQL 下一列都没改到,
	// go-sql-driver 默认回报改变行数 → RowsAffected == 0 → errCoverRaced
	// "该活动的封面刚刚被改过",而根本没有第二个人改过。
	// 判等放在这里而不是把 CAS 改宽:CAS 挡的是真正的并发换图(后到的那次
	// 会把先到的图挤掉且不给它打 detached_at,那张图从此永远留在磁盘上),
	// 那条防线一个字都不能松。
	if act.CoverUrl == coverURL && act.CoverRef == coverRef {
		writeAdminAudit(c, "lottery.activity.cover", actNo, qymodel.ResultOK, "封面未变化",
			before, snapText(map[string]any{"cover_url": coverURL, "cover_ref": coverRef}))
		respondOK(c, gin.H{"act_no": actNo, "cover_url": coverURL, "cover_ref": coverRef})
		return
	}
	err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := bindCover(tx, act.Id, act.ActNo, act.CoverRef, coverRef, adminId); err != nil {
			return err
		}
		// 条件带上"封面还是我读到的那一份":两个管理员同时换图时,后到的那次
		// 会因为 RowsAffected == 0 被拒,而不是把对方刚绑好的图悄悄挤掉 ——
		// 被挤掉的那张会因为没人给它打 detached_at 而永远留在磁盘上。
		res := tx.Model(&Activity{}).
			Where("id = ? AND cover_url = ? AND cover_ref = ?", act.Id, act.CoverUrl, act.CoverRef).
			Updates(map[string]any{
				"cover_url":  coverURL,
				"cover_ref":  coverRef,
				"updated_at": common.GetTimestamp(),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errCoverRaced
		}
		return nil
	})
	if err != nil {
		if _, ok := AsBizError(err); !ok {
			db.MarkFailure(err)
			err = wrapInternal("更新活动封面", err)
		}
		writeAdminAudit(c, "lottery.activity.cover", actNo, qymodel.ResultFail,
			auditReason(err), before, "")
		respondErr(c, err)
		return
	}

	writeAdminAudit(c, "lottery.activity.cover", actNo, qymodel.ResultOK, "", before,
		snapText(map[string]any{"cover_url": coverURL, "cover_ref": coverRef}))
	respondOK(c, gin.H{"act_no": actNo, "cover_url": coverURL, "cover_ref": coverRef})
}

// errCoverRaced 是"你读到的封面已经不是现在这一份"的诚实回答。
// 与 errStatusConflict 分开:那一条说的是活动状态不对,而这里状态完全没问题。
var errCoverRaced = newBizError(http.StatusConflict, "qy_lot_cover_raced",
	"该活动的封面刚刚被改过,请刷新后重试")

// handleGetCover 匿名回一张【已经用在某场活动上的】封面。
//
// 刻意不走 guard.RequireAPI(FlagLottery):娱乐功能被临时关停之后,历史活动页
// 仍然会被打开(证据链端点就是匿名可访问的),那时每一张卡都变成破图没有任何
// 好处。只判扩展是否启用与库是否可用 —— 与 handleGetProof 同一条口径。
//
// **只回已绑定的**:未绑定的上传只属于上传者,匿名可取等于把管理员挑到一半的
// 图公开出去。判定顺序是先"是否已绑定"再"是否已回收",两者都回各自的答案:
// ref 有 128 位熵,不存在靠区分这两个回答来枚举的路径,而把"已回收"糊成
// "不存在"会让运营以为自己传错了图。
func handleGetCover(c *gin.Context) {
	if !guard.Enabled() {
		respondErr(c, errCoverNotFound)
		return
	}
	if !db.Available() {
		c.Header("Retry-After", "30")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": guard.CodeUnavailable, "message": "封面暂不可用,请稍后重试",
		})
		return
	}
	ref := c.Param("ref")
	if ref == "" {
		respondErr(c, errCoverNotFound)
		return
	}
	row, err := loadCover(ref)
	if err != nil {
		respondErr(c, err)
		return
	}
	if row.ActId == 0 || row.BoundAt == 0 {
		respondErr(c, errCoverNotFound)
		return
	}
	if row.PurgedAt > 0 {
		respondErr(c, errCoverPurged)
		return
	}
	serveCover(c, row)
}
