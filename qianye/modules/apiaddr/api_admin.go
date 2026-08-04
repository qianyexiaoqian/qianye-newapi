package apiaddr

import (
	"context"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
	"github.com/QuantumNous/new-api/qianye/service/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// api_admin.go —— API 地址簿的管理端增删改查与整表重排。
//
// # 审计口径
//
// 四个写接口各有**恰好两处** audit.WriteConfigUpdate:成功一条、失败一条。
// 失败那条是必须的 —— "有人在这一刻试图改地址簿、被上限/判重/并发挡下了"
// 与"没人动过"在事后是完全不同的两件事,而只记成功等于把前者伪装成后者。
//
// 纯输入校验失败(名字空、URL 少了 scheme)**不写**审计:那是管理员在输入框里
// 打字的过程,一次编辑会产生十几条,把真正有价值的那几条冲掉。分界线是
// "有没有碰到库里的既有状态"。
//
// 直接用 audit.WriteConfigUpdate 而不再包一层本地 writeXxxAudit:那一层正是
// 本仓刚刚收敛掉的"同一概念的第 N 份拷贝"。
const (
	actionCreate  = "api_address.create"
	actionUpdate  = "api_address.update"
	actionDelete  = "api_address.delete"
	actionReorder = "api_address.reorder"
)

// upsertReq 是新建/编辑的入参。
//
// Enabled 走 *bool 而不是 bool:JSON 里字段缺失与显式 false 在 bool 上都是
// false,于是"新建时不传 enabled"会得到一条建完就用不了的地址,而"编辑时只想
// 改备注"会顺手把它停用。指针让这两件事可分辨 —— 新建时 nil 记作启用
// (建一条立刻不能用的地址没有意义),编辑时 nil 记作保持原样。
type upsertReq struct {
	Name    string `json:"name"`
	Remark  string `json:"remark"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled"`
}

// applyTo 把入参校验并写进目标行。creating 决定 Enabled 缺省时的含义。
func (r *upsertReq) applyTo(dst *Address, creating bool) error {
	name, err := normalizeName(r.Name)
	if err != nil {
		return err
	}
	remark, err := normalizeRemark(r.Remark)
	if err != nil {
		return err
	}
	addrURL, err := normalizeURL(r.URL)
	if err != nil {
		return err
	}
	dst.Name, dst.Remark, dst.URL = name, remark, addrURL
	switch {
	case r.Enabled != nil:
		dst.Enabled = *r.Enabled
	case creating:
		dst.Enabled = true
	}
	return nil
}

// auditSnapshot 是写进审计 before/after 的字段白名单。
//
// 刻意不直接序列化 Address:白名单让"新增字段默认不进审计",而不是反过来 ——
// 审计快照会被原样存库并展示,一个将来加进 Address 的敏感字段会静静地流进去。
type auditSnapshot struct {
	Id        int    `json:"id"`
	Name      string `json:"name"`
	Remark    string `json:"remark"`
	URL       string `json:"url"`
	SortOrder int    `json:"sort_order"`
	Enabled   bool   `json:"enabled"`
}

func snapshotOf(a Address) auditSnapshot {
	return auditSnapshot{
		Id: a.Id, Name: a.Name, Remark: a.Remark,
		URL: a.URL, SortOrder: a.SortOrder, Enabled: a.Enabled,
	}
}

// adminList 下发全部地址(含已停用),按展示顺序。
func adminList(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()

	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}
	rows, err := listAll(ctx, gdb, false)
	if err != nil {
		respondErr(c, err)
		return
	}
	// max 跟着列表一起下发:前端要用它决定「新增」按钮是否可用,自己抄一份
	// 上限就是同一常量的第二份拷贝,改后端时前端不会跟着变。
	respondOK(c, gin.H{"items": rows, "max": maxAddresses})
}

// adminCreate 新增一条地址,追加到列表末尾。
func adminCreate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req upsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	var row Address
	if err := req.applyTo(&row, true); err != nil {
		respondErr(c, err)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	if err := insertAddress(ctx, gdb, &row, c.GetInt("id")); err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionCreate, Result: qymodel.ResultFail,
			Reason: err.Error(), After: snapshotOf(row),
		})
		respondErr(c, err)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: actionCreate, After: snapshotOf(row),
	})
	respondOK(c, row)
}

// adminUpdate 编辑一条地址。排序不在这里改 —— 见 adminReorder。
func adminUpdate(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		return
	}
	var req upsertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	// 纯输入校验先跑,而且必须跑在拿库句柄之前 —— 本文件顶部的审计口径是
	// "碰到库里既有状态的失败才写审计"。updateAddress 内部才调 applyTo,于是
	// 名称清空、URL 少 scheme 这类打字过程会各留一条 result=fail 的
	// api_address.update,把真正有价值的那几条(超上限 / URL 重复 / 并发重排
	// 被拒)冲掉。新建路径本来就是这个顺序,这里补齐,两端口径才一致。
	//
	// 校验落在一份丢弃的行上:creating=true 只影响 Enabled 缺省的含义,
	// 而真正写进库的那次仍然由 updateAddress 用 creating=false 重跑一遍,
	// "编辑时不传 enabled = 保持原样"因此不受影响。
	var probe Address
	if err := req.applyTo(&probe, true); err != nil {
		respondErr(c, err)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	before, after, err := updateAddress(ctx, gdb, id, &req, c.GetInt("id"))
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionUpdate, Result: qymodel.ResultFail,
			Reason: err.Error(), Before: snapshotOf(before),
		})
		respondErr(c, err)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: actionUpdate, Before: snapshotOf(before), After: snapshotOf(after),
	})
	respondOK(c, after)
}

// adminDelete 删除一条地址。
//
// 硬删而不是软删:这张表没有任何外键引用它,历史连接信息早已落在用户的剪贴板与
// 客户端配置里,与本行的存续无关。"删的是哪一条"由审计的 before 快照回答 ——
// 那正是删除类操作里 before 唯一不可替代的价值。
func adminDelete(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	id, ok := pathId(c)
	if !ok {
		return
	}
	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	before, err := loadRow(ctx, gdb, id)
	if err == nil {
		err = gdb.WithContext(ctx).Where("id = ?", id).Delete(&Address{}).Error
		if err != nil {
			db.MarkFailure(err)
		}
	}
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionDelete, Result: qymodel.ResultFail,
			Reason: err.Error(), Before: snapshotOf(before),
		})
		respondErr(c, err)
		return
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: actionDelete, Before: snapshotOf(before),
	})
	respondOK(c, gin.H{"id": id})
}

// orderReq 是整表重排的入参:按目标顺序排列的完整 id 列表。
type orderReq struct {
	Ids []int `json:"ids"`
}

// adminReorder 按提交的完整顺序重排整表。
func adminReorder(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagCore) {
		return
	}
	var req orderReq
	if err := c.ShouldBindJSON(&req); err != nil {
		respondErr(c, errInvalidParam)
		return
	}
	ids, err := normalizeOrderIds(req.Ids)
	if err != nil {
		respondErr(c, err)
		return
	}

	ctx, cancel := guard.ColdContext(c.Request.Context())
	defer cancel()
	gdb, err := handle(ctx)
	if err != nil {
		respondErr(c, err)
		return
	}

	before, err := listAll(ctx, gdb, false)
	if err == nil {
		err = applyOrder(ctx, gdb, ids)
	}
	if err != nil {
		audit.WriteConfigUpdate(c, audit.ConfigChange{
			Action: actionReorder, Result: qymodel.ResultFail,
			Reason: err.Error(), Before: orderSnapshot(before),
		})
		respondErr(c, err)
		return
	}
	after, err := listAll(ctx, gdb, false)
	if err != nil {
		// 重排已经生效,回读失败只影响快照的后半段与响应体。
		common.SysError("qianye/apiaddr: 重排后回读列表失败: " + err.Error())
		after = before
	}
	audit.WriteConfigUpdate(c, audit.ConfigChange{
		Action: actionReorder,
		Before: orderSnapshot(before), After: orderSnapshot(after),
	})
	respondOK(c, gin.H{"items": after, "max": maxAddresses})
}

// orderSnapshot 把一份列表压成审计里的顺序快照。
//
// 只留 id + 名字:重排改的就是顺序本身,把每一行的完整字段再抄一遍只会让
// "顺序变了没有"这件事淹没在噪音里。
func orderSnapshot(rows []Address) []auditSnapshot {
	out := make([]auditSnapshot, 0, len(rows))
	for _, row := range rows {
		out = append(out, auditSnapshot{Id: row.Id, Name: row.Name, SortOrder: row.SortOrder})
	}
	return out
}

// insertAddress 在过完"行数上限"与"URL 判重"两道闸门之后落库。
//
// 单独成函数是为了让 adminCreate 的失败审计只有一个汇合点:三种失败
// (查询出错 / 超上限 / URL 重复)在审计里是同一件事(这次新增没成功),
// 拆成三条 respondErr 就要写三份一模一样的审计,而漏掉其中一份是本仓
// 反复出现的形状。
func insertAddress(ctx context.Context, gdb *gorm.DB, row *Address, actorId int) error {
	count, err := countAll(ctx, gdb)
	if err != nil {
		return err
	}
	if count >= maxAddresses {
		return errLimitReached
	}
	taken, err := urlTaken(ctx, gdb, row.URL, 0)
	if err != nil {
		return err
	}
	if taken {
		return errDuplicateURL
	}
	order, err := nextSortOrder(ctx, gdb)
	if err != nil {
		return err
	}

	now := common.GetTimestamp()
	row.SortOrder = order
	row.CreatedAt, row.UpdatedAt = now, now
	row.CreatedBy, row.UpdatedBy = actorId, actorId
	if err := gdb.WithContext(ctx).Create(row).Error; err != nil {
		db.MarkFailure(err)
		return err
	}
	return nil
}

// updateAddress 读出原行、校验、写回,返回改动前后的两份快照。
//
// 校验放在读之后而不是之前:applyTo 的 Enabled 语义是"缺省保持原样",
// 而"原样"只有拿到原行才知道。
func updateAddress(ctx context.Context, gdb *gorm.DB, id int, req *upsertReq, actorId int) (Address, Address, error) {
	before, err := loadRow(ctx, gdb, id)
	if err != nil {
		return Address{}, Address{}, err
	}
	after := before
	if err := req.applyTo(&after, false); err != nil {
		return before, Address{}, err
	}
	taken, err := urlTaken(ctx, gdb, after.URL, id)
	if err != nil {
		return before, Address{}, err
	}
	if taken {
		return before, Address{}, errDuplicateURL
	}

	after.UpdatedAt = common.GetTimestamp()
	after.UpdatedBy = actorId
	// 显式列清单而不是 Save/Updates(struct):后者会把 sort_order 一并写回,
	// 而排序归 adminReorder 管 —— 两个入口同时能改同一列时,先保存编辑再保存
	// 排序就会把排序覆盖回编辑那一刻的旧值。
	res := gdb.WithContext(ctx).Model(&Address{}).Where("id = ?", id).
		Updates(map[string]any{
			"name": after.Name, "remark": after.Remark, "url": after.URL,
			"enabled": after.Enabled, "updated_at": after.UpdatedAt,
			"updated_by": after.UpdatedBy,
		})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return before, Address{}, res.Error
	}
	if res.RowsAffected == 0 {
		// 行在读与写之间被别人删掉了。报 404 而不是静默返回成功:
		// 后者会让管理员以为改动生效了,而列表刷新之后那一行根本不在。
		return before, Address{}, errNotFound
	}
	return before, after, nil
}

// pathId 解析路径上的 id,非法时直接写响应。
func pathId(c *gin.Context) (int, bool) {
	raw, ok := httpq.PathInt64(c, "id")
	if !ok || raw <= 0 || raw > math.MaxInt32 {
		respondErr(c, errInvalidParam)
		return 0, false
	}
	return int(raw), true
}
