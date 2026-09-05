package mall

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
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
