package sitetheme

import (
	"errors"
	"sync/atomic"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/db"
	qymodel "github.com/QuantumNous/new-api/qianye/model"

	"gorm.io/gorm"
)

const (
	settingScope = "site_theme"
	keyPreset    = "default_preset"
	keyForce     = "force_preset"
)

// DefaultPreset 是未配置时的站点默认,与上游前端硬编码的默认值保持一致。
//
// 刻意不设成 steins-gate:这个包的职责是"让运营能改默认值",而不是替运营
// 做决定。全新部署应当与上游行为一致,改不改由管理员在后台点。
const DefaultPreset = "default"

// allowedPresets 是前端 THEME_PRESETS 里真实存在的取值。
//
// 必须与 web/src/lib/theme-customization.ts 保持同步。写死在这里而不是让
// 前端传什么存什么:存进去一个前端不认识的值,用户会拿到一个无样式的页面,
// 而且从后台看不出哪里错了。
//
// 刻意不含上游已移除的 classic/旧版前端 —— 上游的 UpdateOption 会直接拒绝
// 那个值("Classic 前端已移除"),这里放进来只会产生一个选了报错的选项。
var allowedPresets = map[string]bool{
	"default":        true,
	"steins-gate":    true,
	"anthropic":      true,
	"simple-large":   true,
	"underground":    true,
	"rose-garden":    true,
	"lake-view":      true,
	"sunset-glow":    true,
	"forest-whisper": true,
	"ocean-breeze":   true,
	"lavender-dream": true,
}

// ErrUnknownPreset 表示提交了一个前端不存在的预设。
var ErrUnknownPreset = errors.New("qianye: 未知的主题预设")

// snapshot 是进程内缓存。
//
// 站点默认会被每一次 GET /api/qy/config 读到(前端引导端点),而那是所有页面
// 加载都会打的接口 —— 每次查库不可接受。配置变更时主动失效,不做 TTL:
// 这个值极少变,过期刷新只会让"刚改完看不到效果"变成必然。
var snapshot atomic.Pointer[settings]

type settings struct {
	Preset string
	Force  bool
}

// Current 返回当前站点主题设置。
//
// 扩展库不可用时返回上游默认值而不是报错:主题是展示层配置,
// 拿不到就退回上游行为,绝不能让整个引导端点失败。
func Current() (preset string, force bool) {
	if s := snapshot.Load(); s != nil {
		return s.Preset, s.Force
	}
	s := loadFromDB()
	snapshot.Store(s)
	return s.Preset, s.Force
}

func loadFromDB() *settings {
	s := &settings{Preset: DefaultPreset}
	gdb := db.Get()
	if gdb == nil || !db.Available() {
		return s
	}
	var rows []qymodel.Setting
	if err := gdb.Where("scope = ?", settingScope).Find(&rows).Error; err != nil {
		db.MarkFailure(err)
		// 读失败回落到上游默认。绝不能回落成"上次缓存的值"——
		// 那会让一次抖动把一个已被管理员改掉的主题又端出来。
		return s
	}
	for _, r := range rows {
		switch r.K {
		case keyPreset:
			if allowedPresets[r.V] {
				s.Preset = r.V
			}
		case keyForce:
			s.Force = r.V == "true"
		}
	}
	return s
}

// save 写入设置并立即失效缓存。
func save(preset string, force bool, operatorId int) error {
	if !allowedPresets[preset] {
		return ErrUnknownPreset
	}
	gdb := db.Get()
	if gdb == nil {
		return db.ErrNotReady
	}
	now := common.GetTimestamp()
	err := gdb.Transaction(func(tx *gorm.DB) error {
		for k, v := range map[string]string{
			keyPreset: preset,
			keyForce:  boolStr(force),
		} {
			row := qymodel.Setting{
				Scope: settingScope, K: k, V: v,
				OperatorId: operatorId, UpdatedAt: now,
			}
			if err := tx.Save(&row).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		db.MarkFailure(err)
		return err
	}
	snapshot.Store(&settings{Preset: preset, Force: force})
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// AllowedPresets 返回全部合法预设,供管理端下拉与校验共用同一份事实。
func AllowedPresets() []string {
	out := make([]string, 0, len(allowedPresets))
	for k := range allowedPresets {
		out = append(out, k)
	}
	return out
}
