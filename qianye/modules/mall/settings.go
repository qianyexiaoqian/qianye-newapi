package mall

import (
	"context"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"
)

// settings.go —— 运营可覆盖的参数(qy_settings, scope=mall),照 lottery/settings.go
// 的最小形状:本模块只有 show_entry 一个键(design §9.2)。
//
// 引导端点 /api/qy/config 默认只读 YAML;本模块在 InstallHooks 里把
// qyctl.QyMallEntryShown 换成这里的合并结果 —— 不接管就会出现"运营关掉了入口,
// 前台照旧显示"。

const (
	settingScope = "mall"
	keyShowEntry = "show_entry"
)

const settingsCacheSeconds = 60

var (
	settingsMu     sync.Mutex
	settingsCache  *bool
	settingsLoaded int64
)

// entryShown 回答"前端要不要渲染商城入口":YAML 基线 + qy_settings 覆盖。
//
// 读不到覆盖值时退回 YAML,而不是让引导端点报错 —— 少一个运营微调远比
// "整个前端拿不到引导配置"轻。
func entryShown() bool {
	base := config.Get().Mall.EntryShown()

	settingsMu.Lock()
	if settingsCache != nil && common.GetTimestamp()-settingsLoaded < settingsCacheSeconds {
		v := *settingsCache
		settingsMu.Unlock()
		return v
	}
	settingsMu.Unlock()

	gdb := db.Get()
	if gdb == nil {
		return base
	}
	// 自带冷路径预算而不是裸查:引导端点是匿名首屏,一条卡住的 SELECT 会把
	// 整个前端的启动挂在扩展库的 readTimeout 上。
	ctx, cancel := guard.ColdContext(context.Background())
	defer cancel()
	rows := make([]qymodel.Setting, 0, 1)
	if err := gdb.WithContext(ctx).
		Where("scope = ? AND k = ?", settingScope, keyShowEntry).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		return base
	}
	merged := base
	if len(rows) == 1 {
		// 读不懂的行(被人手改成 "yes")一律丢弃并回落基线:一行读不懂的配置
		// 不该让商城入口从站点上消失,那种消失没有任何一处会报错。
		switch strings.ToLower(strings.TrimSpace(rows[0].V)) {
		case "1", "true":
			merged = true
		case "0", "false":
			merged = false
		}
	}
	settingsMu.Lock()
	settingsCache = &merged
	settingsLoaded = common.GetTimestamp()
	settingsMu.Unlock()
	return merged
}

// invalidateSettings 清掉进程内缓存(测试与将来的写接口用)。
func invalidateSettings() {
	settingsMu.Lock()
	settingsCache = nil
	settingsLoaded = 0
	settingsMu.Unlock()
}
