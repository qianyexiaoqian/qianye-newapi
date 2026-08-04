package config

import (
	"time"

	"github.com/QuantumNous/new-api/common"

	"github.com/bytedance/gopkg/util/gopool"
)

// Reload 重新读取配置文件并替换当前快照。
//
// Database 段与 Runtime.LeaseTTLSeconds 一律沿用旧值:连接池与 DSN 不能热切换,
// 那会让正在进行的事务落到已关闭的旧连接上;租约 TTL 中途变化则会让
// 已经在跑的续租协程与新周期不一致,可能造成任务被误判为失效。
//
// 解析或校验失败时保持原配置不变并返回错误 —— 绝不能因为一次误编辑
// 就让运行中的服务丢失全部配置。
func Reload() error {
	path, found := resolvePath()
	if !found {
		if path != "" {
			return errConfigMissing(path)
		}
		// 原本就没有配置文件,没什么可重载的。
		return nil
	}
	fresh, mod, err := parseFile(path)
	if err != nil {
		return err
	}

	old := Get()
	fresh.Database = old.Database
	fresh.Runtime.LeaseTTLSeconds = old.Runtime.LeaseTTLSeconds
	// 已启动的扩展不允许通过热载被整体关停:那会让正在处理的资金操作
	// 突然失去后端支撑。要停用请重启进程。
	if old.Enabled {
		fresh.Enabled = true
	}

	current.Store(fresh)
	loadedPath.Store(path)
	loadedAt.Store(common.GetTimestamp())
	loadedMod.Store(mod)
	common.SysLog("qianye: 配置已重新加载(数据库段保持不变)")
	// 热载同样要过一遍段级自检。一次误编辑把整段删掉,与启动时少一段是同一个
	// 缺陷,而热载不重启进程 —— 启动日志里那一行永远不会再出现。
	logModuleSections(fresh)
	return nil
}

// WatchReload 按 config_reload_seconds 轮询文件修改时间,变化时自动重载。
//
// 刻意不引入 fsnotify:那是新增依赖,而轮询 mtime 对配置文件这种低频变更完全够用,
// 也与上游 model.SyncOptions 的做法一致。
func WatchReload() {
	interval := Get().Runtime.ConfigReloadSeconds
	if interval <= 0 {
		return
	}
	gopool.Go(func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			path, found := resolvePath()
			if !found {
				continue
			}
			mod := fileModTime(path)
			if mod == 0 || mod == loadedMod.Load() {
				continue
			}
			if err := Reload(); err != nil {
				common.SysError("qianye: 自动重载配置失败,继续沿用原配置: " + err.Error())
			}
		}
	})
}

func fileModTime(path string) int64 {
	st, err := osStat(path)
	if err != nil {
		return 0
	}
	return st.ModTime().Unix()
}
