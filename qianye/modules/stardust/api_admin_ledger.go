package stardust

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	"github.com/QuantumNous/new-api/qianye/httpq"
	"github.com/QuantumNous/new-api/qianye/modules/invite"

	"github.com/gin-gonic/gin"
)

// api_admin_ledger.go —— 管理端的三张只读列表:余额 / 流水 / 日桶。
//
// 跨库不能 JOIN:用户名从主库按本页的 user_id 批量取一次;keyword 检索先到主库
// 找出 id 列表,再拿它筛扩展库的余额行。

func init() {
	adminRouteInstallers = append(adminRouteInstallers, func(g *gin.RouterGroup) {
		g.GET("/stardust/balances", handleAdminListBalances)
		g.GET("/stardust/ledger", handleAdminListLedger)
		g.GET("/stardust/accruals", handleAdminListAccruals)
	})
}

// maxKeywordMatches 是一次 keyword 检索最多带回多少个主库 id。
// 关键词命中上千人时这张表已经没法看了,运营要做的是换一个更具体的词。
const maxKeywordMatches = 500

// adminBalanceView 是余额列表的一行:用户端形状之外多 user_id / username / updated_at。
type adminBalanceView struct {
	UserId   int    `json:"user_id"`
	Username string `json:"username"`
	balanceView
	UpdatedAt int64 `json:"updated_at"`
}

// adminLedgerView / adminAccrualView:用户端形状 + user_id。
type adminLedgerView struct {
	UserId int `json:"user_id"`
	ledgerView
}

type adminAccrualView struct {
	UserId int `json:"user_id"`
	accrualView
}

// usernamesOf 从主库按 id 批量取用户名。Unscoped:余额行不随账号删除而删除,
// 一个已删账号的余额行仍要能显示它当初叫什么。
func usernamesOf(ctx context.Context, ids []int) (map[int]string, error) {
	names := make(map[int]string, len(ids))
	if len(ids) == 0 {
		return names, nil
	}
	if model.DB == nil {
		return nil, db.ErrNotReady
	}
	var users []model.User
	if err := model.DB.WithContext(ctx).Unscoped().Model(&model.User{}).
		Select("id", "username").Where("id IN ?", ids).Find(&users).Error; err != nil {
		return nil, wrapInternal("读取用户名", err)
	}
	for _, u := range users {
		names[u.Id] = u.Username
	}
	return names, nil
}

// handleAdminListBalances 分页返回余额行,可按 user_id 精确筛、按 keyword(id / 用户名 /
// 邮箱前缀)检索。
func handleAdminListBalances(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&Balance{})
	if uid := httpq.Int(c, "user_id", 0); uid > 0 {
		q = q.Where("user_id = ?", uid)
	}
	if kw := strings.TrimSpace(c.Query("keyword")); kw != "" {
		if model.DB == nil {
			respondErr(c, db.ErrNotReady)
			return
		}
		// 纯数字优先当 id 精确匹配,同时仍然 OR 上用户名 / 邮箱的前缀匹配 ——
		// 用户名叫 "123" 的账号确实存在。检索走 httpq.SearchLike:转义通配符、折叠大小写,
		// 三种数据库同一口径。
		expr, pattern := httpq.SearchLike(kw, httpq.MatchPrefix, "username", "email")
		uq := model.DB.WithContext(ctx).Unscoped().Model(&model.User{})
		if id := httpq.Int(c, "keyword", 0); id > 0 {
			uq = uq.Where("id = ? OR "+expr, id, pattern, pattern)
		} else {
			uq = uq.Where(expr, pattern, pattern)
		}
		ids := make([]int, 0, maxKeywordMatches)
		if err := uq.Order("id asc").Limit(maxKeywordMatches).Pluck("id", &ids).Error; err != nil {
			respondErr(c, wrapInternal("按关键词检索用户", err))
			return
		}
		if len(ids) == 0 {
			respondOK(c, gin.H{"items": make([]adminBalanceView, 0), "total": 0, "page": page, "page_size": size})
			return
		}
		q = q.Where("user_id IN ?", ids)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计余额行", err))
		return
	}
	rows := make([]Balance, 0, size)
	if err := q.Order("updated_at desc, user_id desc").Offset(httpq.Offset(page, size)).
		Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询余额行", err))
		return
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserId)
	}
	names, err := usernamesOf(ctx, ids)
	if err != nil {
		respondErr(c, err)
		return
	}
	items := make([]adminBalanceView, 0, len(rows))
	for _, r := range rows {
		items = append(items, adminBalanceView{
			UserId: r.UserId, Username: names[r.UserId], balanceView: newBalanceView(r), UpdatedAt: r.UpdatedAt,
		})
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleAdminListLedger 分页返回流水,可按 user_id / kind / act_no 筛。
func handleAdminListLedger(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&Ledger{})
	if uid := httpq.Int(c, "user_id", 0); uid > 0 {
		q = q.Where("user_id = ?", uid)
	}
	if kind := strings.TrimSpace(c.Query("kind")); kind != "" {
		if _, err := kindColumn(Kind(kind)); err != nil {
			respondErr(c, errBadRequest("未知的流水种类: "+kind))
			return
		}
		q = q.Where("kind = ?", kind)
	}
	if actNo := strings.TrimSpace(c.Query("act_no")); actNo != "" {
		q = q.Where("act_no = ?", clip(actNo, 32))
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计流水", err))
		return
	}
	rows := make([]Ledger, 0, size)
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询流水", err))
		return
	}
	items := make([]adminLedgerView, 0, len(rows))
	for _, r := range rows {
		items = append(items, adminLedgerView{UserId: r.UserId, ledgerView: newLedgerView(r)})
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}

// handleAdminListAccruals 分页返回日桶,可按 user_id / bucket_date / status 筛。
func handleAdminListAccruals(c *gin.Context) {
	if !guard.RequireAPI(c, guard.FlagStardust) {
		return
	}
	ctx := c.Request.Context()
	page, size := httpq.Paginate(c, listPaging)
	gdb := db.Get()
	if gdb == nil {
		respondErr(c, db.ErrNotReady)
		return
	}

	q := gdb.WithContext(ctx).Model(&Accrual{})
	if uid := httpq.Int(c, "user_id", 0); uid > 0 {
		q = q.Where("user_id = ?", uid)
	}
	if day := strings.TrimSpace(c.Query("bucket_date")); day != "" {
		// 桶日的合法性由生成它的同一个日界包裁定(DayKeyStart 解析不了的就不是一个桶日),
		// 不在这里另写一份"八位数字"的形状判断。
		if _, ok := invite.DayKeyStart(day); !ok {
			respondErr(c, errBadRequest("bucket_date 必须是 YYYYMMDD"))
			return
		}
		q = q.Where("bucket_date = ?", day)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		if status != AccrualComputed && status != AccrualSettled && status != AccrualHeld {
			respondErr(c, errBadRequest("未知的日桶状态: "+status))
			return
		}
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("统计日桶", err))
		return
	}
	rows := make([]Accrual, 0, size)
	if err := q.Order("bucket_date desc, id desc").Offset(httpq.Offset(page, size)).
		Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		respondErr(c, wrapInternal("查询日桶", err))
		return
	}
	items := make([]adminAccrualView, 0, len(rows))
	for _, r := range rows {
		items = append(items, adminAccrualView{UserId: r.UserId, accrualView: newAccrualView(r)})
	}
	respondOK(c, gin.H{"items": items, "total": total, "page": page, "page_size": size})
}
