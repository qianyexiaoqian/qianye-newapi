package config

import (
	"fmt"
	"os"

	"github.com/QuantumNous/new-api/common"
)

// EnvConfigPath 是显式指定配置文件路径的环境变量名。
const EnvConfigPath = "QIANYE_CONFIG"

// searchPaths 是未显式指定时的查找顺序。
//
// Docker 下容器 WORKDIR=/data 且 docker-compose 把宿主 ./data 挂到 /data,
// 因此 ./qianye.yaml 在容器内即宿主机的 ./data/qianye.yaml。
// 本地开发则命中 ./data/qianye.yaml —— .gitignore 已忽略 /data/,
// 配置里的数据库密码不会被误提交。
var searchPaths = []string{
	"./qianye.yaml",
	"./data/qianye.yaml",
}

// resolvePath 定位配置文件。
//
// 返回值 (path, found):
//   - (p, true)  找到了,p 是实际路径
//   - (p, false) QIANYE_CONFIG 显式指定为 p 但文件不存在 —— 调用方须报错
//   - ("", false) 没有显式指定且默认路径都不存在 —— 调用方须静默禁用扩展
//
// 区分后两者很重要:显式配错路径是运维事故必须炸,没配置则是正常的"不启用扩展"。
func resolvePath() (string, bool) {
	if p := common.GetEnvOrDefaultString(EnvConfigPath, ""); p != "" {
		if isFile(p) {
			return p, true
		}
		return p, false
	}
	for _, p := range searchPaths {
		if isFile(p) {
			return p, true
		}
	}
	return "", false
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// osStat 是 os.Stat 的别名,便于其他文件复用而不必各自 import os。
var osStat = os.Stat

// errConfigMissing 构造"显式指定的配置文件不存在"错误。
func errConfigMissing(path string) error {
	return fmt.Errorf("qianye: %s 指向的配置文件不存在: %s", EnvConfigPath, path)
}
