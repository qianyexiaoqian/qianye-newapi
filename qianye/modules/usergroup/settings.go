package usergroup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/qianye/db"
	"github.com/QuantumNous/new-api/qianye/guard"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// settingScope 是本模块在共享 qy_settings 表里的命名空间。
const settingScope = "usergroup"

// keyDefaultGroup 是唯一一个键。空值(或没有这一行)= 未配置。
const keyDefaultGroup = "default_group"

// cacheSeconds 是配置的进程内缓存周期。
//
// 刻意不起后台协程去刷:注册是极低频事件,惰性刷新足够,而每个节点多一个
// 裸 goroutine 是纯负担。管理端保存后会直接把新值写进缓存,不必等这 60 秒。
const cacheSeconds = 60

var (
	cacheMu     sync.Mutex
	cachedGroup string
	// cachedAt 为 0 表示缓存从未成功填充过(冷启动或历次读取都失败)。
	cachedAt int64
)

// currentDefaultGroup 返回运营配置的默认分组,"" 表示未配置。
//
// 读取失败时返回上一次成功读到的快照(冷启动时即空串),绝不返回错误 ——
// 调用方在主库事务里,没有任何合理的错误处理方式,只能 fail-open。
func currentDefaultGroup() string {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	now := common.GetTimestamp()
	if cachedAt > 0 && now-cachedAt < cacheSeconds {
		return cachedGroup
	}
	value, ok := readDefaultGroup()
	if !ok {
		return cachedGroup
	}
	cachedGroup, cachedAt = value, now
	return cachedGroup
}

// storeDefaultGroup 在管理端写入成功后直接刷新缓存。
//
// 比「失效缓存等下次惰性重读」更好的地方在于:重读可能因为扩展库瞬时抖动而
// 失败,那时会退回上一次快照,表现为「保存成功了但新用户还是进旧分组」。
func storeDefaultGroup(value string) {
	cacheMu.Lock()
	cachedGroup, cachedAt = value, common.GetTimestamp()
	cacheMu.Unlock()
}

// resetCache 让下一次读取重新回源。仅测试与配置重载使用。
func resetCache() {
	cacheMu.Lock()
	cachedGroup, cachedAt = "", 0
	cacheMu.Unlock()
}

// warmDefaultGroup 在 Init() 阶段预热一次缓存。
func warmDefaultGroup() {
	if group := currentDefaultGroup(); group != "" {
		common.SysLog("qianye/usergroup: 新用户默认分组已配置为 " + group)
	}
}

// readDefaultGroup 从扩展库回源。第二个返回值表示「本次真的读到了」——
// 未配置(无此行)算读到了,读失败不算。
//
// # 为什么不走 guard.Hot
//
// guard.Hot 在扩展库不可用时会调 recordSkip,而那个计数器的语义是「用户该拿
// 的佣金永久丢失了」,健康面板据此告警。注册回落到上游默认分组不属于这一类:
// 没有任何东西丢失,下一次注册会重新尝试。把它记进同一个计数器会让真正的
// 佣金丢失被淹没在噪音里。这里需要的只是 guard.Hot 的另外三样能力
// —— 硬超时、吞 panic、熔断联动 —— 下面逐条自己接上。
func readDefaultGroup() (string, bool) {
	defer func() {
		if r := recover(); r != nil {
			common.SysError(fmt.Sprintf(
				"qianye/usergroup: 读取默认分组配置发生 panic(已拦截,注册流程不受影响): %v", r))
		}
	}()

	if !guard.Available() {
		return "", false
	}
	gdb := db.Get()
	if gdb == nil {
		return "", false
	}

	// ctx 必须一路透传给 GORM:少接这一处,扩展库卡住时这条语句会一直等到
	// MySQL 的 innodb_lock_wait_timeout(默认 50 秒),而它正压在用户创建的
	// 主库事务里 —— 注册会连带卡死 50 秒。
	ctx, cancel := context.WithTimeout(context.Background(), loadBudget())
	defer cancel()

	var row qymodel.Setting
	err := gdb.WithContext(ctx).
		Where("scope = ? AND k = ?", settingScope, keyDefaultGroup).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", true
	}
	if err != nil {
		db.MarkFailure(err)
		return "", false
	}
	db.MarkSuccess()
	return strings.TrimSpace(row.V), true
}

// loadBudget 复用 runtime.hot_path_timeout_ms:它的语义正是「一个 hook 允许
// 阻塞调用方多久」,而本 hook 阻塞的是用户创建事务,比 relay 更不能久等。
// 不新增配置项 —— 多一个开关就多一个「定义了却没人读」的风险面。
func loadBudget() time.Duration {
	ms := config.Get().Runtime.HotPathTimeoutMs
	if ms <= 0 {
		ms = 200
	}
	return time.Duration(ms) * time.Millisecond
}

// writeDefaultGroup 落一条运营覆盖。调用方负责写审计。
//
// 空串是合法值,表示「取消配置、回到上游默认」。刻意写空行而不是删行:
// 保留 operator_id 与 updated_at 才能在审计之外还看得出「谁在什么时候
// 把它关掉了」。
func writeDefaultGroup(value string, operatorId int) error {
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	row := qymodel.Setting{
		Scope:      settingScope,
		K:          keyDefaultGroup,
		V:          value,
		OperatorId: operatorId,
		UpdatedAt:  common.GetTimestamp(),
	}
	err := gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope"}, {Name: "k"}},
		DoUpdates: clause.AssignmentColumns([]string{"v", "operator_id", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	storeDefaultGroup(value)
	return nil
}
