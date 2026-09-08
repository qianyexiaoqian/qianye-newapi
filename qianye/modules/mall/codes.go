package mall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/httpq"

	"gorm.io/gorm"
)

// codes.go —— 兑换码库存的批量上传(design §6.1)。
//
// 入库即密文,明文只出现在上传请求体里(这条路由登记 credentialBodyRoutes,请求台账
// 不存 body)。码的内容平台不解析、不能识别;唯一的机器闸门是"不得上架本站余额码"
// (D-K):对每条明文点查一次主库 redemptions.key,命中即拒 —— 那是"星屑 → 码 →
// users.quota"隔了一跳,绕过"星屑不可兑回"的拍板。

// maxCodeRunes 是单枚兑换码的长度上限。密文列没有长度上界,上界由这里给。
const maxCodeRunes = 128

// codeReject 是被拒的一条:第几条、为什么。绝不回显码本身。
type codeReject struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// uploadCodes 把一批明文兑换码封成密文入库。返回入库条数与被拒清单。
//
// 被拒的条目不阻塞其余条目:运营一次粘贴几百行,一行空白就整批打回是折磨人。
// 只有加密失败(密钥缺失)才整批失败 —— 那是运维事故,不是数据问题。
func uploadCodes(ctx context.Context, p *Product, codes []string) (int, []codeReject, error) {
	if p.Kind != KindCode {
		return 0, nil, errBadRequest("只有兑换码商品可以上传兑换码")
	}
	limit := config.Get().Mall.CodeUploadMax
	if limit <= 0 {
		limit = 500
	}
	if len(codes) == 0 {
		return 0, nil, errBadRequest("codes 不能为空")
	}
	if len(codes) > limit {
		return 0, nil, errBadRequest(fmt.Sprintf("一次最多上传 %d 条兑换码(mall.code_upload_max)", limit))
	}
	handle := db.Get()
	if handle == nil {
		return 0, nil, db.ErrNotReady
	}
	gdb := handle.WithContext(ctx)

	now := common.GetTimestamp()
	rejected := make([]codeReject, 0)
	rows := make([]CodeStock, 0, len(codes))
	seen := make(map[string]bool, len(codes))
	for i, raw := range codes {
		plain := strings.TrimSpace(raw)
		switch {
		case plain == "":
			rejected = append(rejected, codeReject{Index: i, Reason: "empty"})
			continue
		case utf8.RuneCountInString(plain) > maxCodeRunes:
			rejected = append(rejected, codeReject{Index: i, Reason: "too_long"})
			continue
		case seen[plain]:
			rejected = append(rejected, codeReject{Index: i, Reason: "duplicate"})
			continue
		}
		seen[plain] = true
		site, err := isSiteRedemptionCode(ctx, plain)
		if err != nil {
			return 0, nil, err
		}
		if site {
			rejected = append(rejected, codeReject{Index: i, Reason: "本站兑换码不可上架"})
			continue
		}
		row := CodeStock{ProductId: p.Id, Status: CodeUnused, CreatedAt: now}
		if err := sealCode(&row, plain, p.ProductNo); err != nil {
			return 0, nil, err
		}
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		if err := gdb.CreateInBatches(rows, 200).Error; err != nil {
			db.MarkFailure(err)
			return 0, nil, wrapInternal("写入兑换码库存", err)
		}
	}
	return len(rows), rejected, nil
}

// isSiteRedemptionCode 回答"这串码是不是本站主库 redemptions 里的兑换码"。
//
// 用结构体条件而不是拼列名:`key` 在 MySQL 上是保留字,GORM 对结构体 / map 条件里的
// 列名会按方言加引号。Unscoped:已软删的码同样是本站的码。主库查不到时整批拒绝
// 而不是放行 —— 放行等于把这道闸门做成"主库抖一下就失效"。
func isSiteRedemptionCode(ctx context.Context, plain string) (bool, error) {
	if model.DB == nil {
		return false, db.ErrNotReady
	}
	var n int64
	if err := model.DB.WithContext(ctx).Unscoped().Model(&model.Redemption{}).
		Where(&model.Redemption{Key: plain}).Count(&n).Error; err != nil {
		return false, wrapInternal("核对主库兑换码", err)
	}
	return n > 0, nil
}

// listCodes 分页返回一件商品的码库存行。status 为空 = 不筛。
//
// 回给前端的是 codeStockView 挑出来的字段,**没有任何一列密文**:
// CodeStock 上那三列虽然带 json:"-",但一旦有人把这里改成直接 Find 出结构体再
// 整行下发,后来者只会看到"它一直是这么写的"。挑字段是让泄漏需要一次显式的新增。
func listCodes(ctx context.Context, gdb *gorm.DB, p *Product, status string, page, size int) ([]CodeStock, int64, error) {
	if p.Kind != KindCode {
		return nil, 0, errBadRequest("只有兑换码商品有码库存")
	}
	q := gdb.WithContext(ctx).Model(&CodeStock{}).Where("product_id = ?", p.Id)
	switch status {
	case "":
	case CodeUnused, CodeIssued, CodeRevoked, CodeTaken:
		q = q.Where("status = ?", status)
	default:
		return nil, 0, errBadRequest("status 只能是 unused / issued / revoked / taken")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		db.MarkFailure(err)
		return nil, 0, wrapInternal("统计兑换码库存", err)
	}
	rows := make([]CodeStock, 0, size)
	// id desc:最近入库的排在前面,运营刚粘完一批就想核对的就是那一批。
	if err := q.Order("id desc").Offset(httpq.Offset(page, size)).Limit(size).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return nil, 0, wrapInternal("查询兑换码库存", err)
	}
	return rows, total, nil
}

// takeCode 由管理员从库里提走一枚**未使用**的码:解出明文并把它标成 taken。
//
// # 为什么必须在同一个事务里连锁带改
//
// 解密与改状态之间若隔着一次提交,两个管理员可以同时提走同一枚码,两个人各拿到
// 一份明文,而库里只留下一次状态变更 —— 事后无从知道那枚码流到了几个人手上。
// 行锁 + `status = 'unused'` 的条件读把这一步压成一次:第二个人读到的是
// "已经不是 unused",拿到 409。
//
// # 为什么先解密再改状态
//
// 解不开(密钥版本没登记、密文被搬过)时这枚码对谁都没有价值,把它标成 taken 只会
// 让一枚还能被修复的码永久离开可售库存。顺序反过来才是"先扣后给"。
func takeCode(ctx context.Context, p *Product, id int64, adminId int) (string, *CodeStock, error) {
	if p.Kind != KindCode {
		return "", nil, errBadRequest("只有兑换码商品有码库存")
	}
	handle := db.Get()
	if handle == nil {
		return "", nil, db.ErrNotReady
	}
	var (
		plain string
		row   CodeStock
	)
	err := handle.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := db.LockForUpdate(tx).
			Where("id = ? AND product_id = ?", id, p.Id).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errCodeNotFound
			}
			return err
		}
		if row.Status != CodeUnused {
			return errCodeNotTakeable
		}
		opened, err := openCode(&row, p.ProductNo)
		if err != nil {
			return err
		}
		plain = opened
		now := common.GetTimestamp()
		res := tx.Model(&CodeStock{}).Where("id = ? AND status = ?", row.Id, CodeUnused).
			Updates(map[string]any{"status": CodeTaken, "taken_at": now, "taken_by": adminId})
		if res.Error != nil {
			return res.Error
		}
		// SQLite 上 LockForUpdate 退化成空操作(没有行锁),条件更新的影响行数是那里
		// 唯一还剩下的并发判据 —— 0 行意味着另一个事务刚把它领走。
		if res.RowsAffected == 0 {
			return errCodeNotTakeable
		}
		row.Status, row.TakenAt, row.TakenBy = CodeTaken, now, adminId
		return nil
	})
	if err != nil {
		return "", nil, bizOrInternal("提取兑换码", err)
	}
	return plain, &row, nil
}

// deleteCode 删掉一枚**未使用**的码。
//
// issued / revoked / taken 一律不许删:前两者是"这个人拿到的是哪一枚"的履行证据,
// taken 是"这枚码被哪个管理员提走了"的去向证据。删商品时只清 unused 也是同一条
// 理由(handleAdminDeleteProduct)。
func deleteCode(ctx context.Context, gdb *gorm.DB, p *Product, id int64) error {
	if p.Kind != KindCode {
		return errBadRequest("只有兑换码商品有码库存")
	}
	var row CodeStock
	if err := gdb.WithContext(ctx).Select("id", "status").
		Where("id = ? AND product_id = ?", id, p.Id).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errCodeNotFound
		}
		db.MarkFailure(err)
		return wrapInternal("读取兑换码库存", err)
	}
	if row.Status != CodeUnused {
		return errCodeNotDeletable
	}
	res := gdb.WithContext(ctx).Where("id = ? AND status = ?", id, CodeUnused).Delete(&CodeStock{})
	if res.Error != nil {
		db.MarkFailure(res.Error)
		return wrapInternal("删除兑换码", res.Error)
	}
	if res.RowsAffected == 0 {
		return errCodeNotDeletable
	}
	return nil
}
