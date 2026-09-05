package invite

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm/clause"
)

// settingScope 是本模块在共享 qy_settings 表里的命名空间。
//
// 本包不再有任何运营可改的键(费率全在星屑那一侧),这里只剩一把自动生成的盐。
const settingScope = "invite"

// keyRefSalt 是下线对外标识(invitee_ref)的 HMAC 密钥在 qy_settings 里的键。
const keyRefSalt = "invitee_ref_salt"

var (
	saltOnce  sync.Mutex
	saltCache string
)

// refSalt 返回下线标识的 HMAC 密钥。
//
// 首次使用时自动生成并持久化。刻意不放 YAML:它必须"部署一次、永不轮换"
// (轮换会让所有历史 ref 失效),自动生成比依赖运维记得改默认值更可靠。
//
// 查库与首次生成都在 saltOnce 之外完成,只有写缓存那一步持锁。持锁查库时,
// 首次调用撞上一次慢查询会把所有关系写入协程一起钉在这把锁上。
// 自带冷路径预算,唯一调用点在关系快照写入路径上,拿不到调用方 ctx。
func refSalt() string {
	saltOnce.Lock()
	cached := saltCache
	saltOnce.Unlock()
	if cached != "" {
		return cached
	}
	gdb := db.Get()
	if gdb == nil {
		return ""
	}
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()

	var row qymodel.Setting
	err := gdb.WithContext(ctx).Where("scope = ? AND k = ?", settingScope, keyRefSalt).
		First(&row).Error
	if err != nil || row.V == "" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			common.SysError("qianye: 生成下线标识盐失败: " + err.Error())
			return ""
		}
		// DoNothing:多节点同时首启(以及本进程内多个协程同时首次调用)时
		// 只有一个写入生效,之后统一重读,保证全网取到同一个盐。
		create := qymodel.Setting{
			Scope: settingScope, K: keyRefSalt, V: hex.EncodeToString(buf),
			UpdatedAt: common.GetTimestamp(),
		}
		if err := gdb.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).
			Create(&create).Error; err != nil {
			db.MarkFailure(err)
			return ""
		}
		if err := gdb.WithContext(ctx).Where("scope = ? AND k = ?", settingScope, keyRefSalt).
			First(&row).Error; err != nil {
			return ""
		}
	}
	if row.V == "" {
		return ""
	}
	saltOnce.Lock()
	if saltCache == "" {
		saltCache = row.V
	}
	value := saltCache
	saltOnce.Unlock()
	return value
}
