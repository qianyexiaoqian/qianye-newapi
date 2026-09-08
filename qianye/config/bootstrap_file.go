package config

// bootstrap_file.go —— 引导向导写出的第一份配置。
//
// # 为什么这份文件由代码生成,而不是让运维照抄 qianye.example.yaml
//
// example.yaml 是 661 行的**说明书**,不是模板:它把每一个旋钮连同代价一起写下来,
// 照抄它等于让第一次部署的人在 200 多个键里挑出那 8 个必填的。而配置是严格解析,
// 抄漏一个非零必填项的表现是进程起不来,抄错一个键名的表现也是进程起不来。
//
// 本文件生成的是**能启动的最小集**:地基三段(database / runtime / two_phase)
// 加上全部业务模块显式关闭。业务模块的开关可以热载(InstallHooks 无条件安装,
// 模块 enabled 是请求时才查),所以"先把库接上,再逐个开功能"不需要第二次重启。
//
// # 为什么连着把 AES 密钥一起生成
//
// mall.secret_key 与 lottery.prize_secret_key 是 base64 的 32 字节 AES-256 密钥,
// 开启对应模块时**必填**(见 validate.go 的 checkAESKey)。手搓它至少有三种错法:
// 长度不对、不是合法 base64、以及把同一把钥匙填进 *_retired 表。等到运维想开商城
// 那天才在启动失败里读到这条,配置界面早就关了。这里预先生成好,模块开关翻 true
// 就能直接用。写进一个 enabled: false 的段是安全的:校验对关闭的模块整段跳过。
//
// # 铁律:写盘前必须自检
//
// Render 拿生成的字节走 parseBytes —— 与进程启动**逐字相同**的那条判据。
// 自检不过就不写盘。这条不能省:向导写出一份让进程起不来的配置之后,
// 界面已经关了,运维手上只有一句"容器启动失败"。

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"

	driver "github.com/go-sql-driver/mysql"
)

// BootstrapParams 是向导收集到的连接参数。
//
// 刻意收参数而不是收一整条 DSN:DSN 的转义规则(密码里的 @ 与 /)是这套东西里
// 最容易静默出错的一处,让人在输入框里手拼等于把这个坑原样交出去。
type BootstrapParams struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
}

// MySQLDSN 拼出扩展库 DSN 并验证驱动能把每一个字段原样解回来。
//
// # 回环校验守的是"某个字段把自己写成了 DSN 语法"
//
// 实测(go-sql-driver v1.7.0)划出的界线与直觉不同,所以判据只能交给驱动本身:
//
//   - **密码**里的 / 与 @ 是安全的。驱动按最后一个 @ 切凭据,连
//     `p@tcp(evil.com:3306)/other` 这种明显想改写地址的密码,解出来的 Addr
//     依然是我们拼进去的那一个。曾以为要拦的这一类,其实不必拦。
//   - **库名、主机、用户名**里的 ? : @ / 括号会真的串场:库名写成
//     `qianye?charset=latin1` 会静默改掉整个连接的字符集,主机写成
//     `evil.com:1)/db?a=b&(` 会把地址与库名一起改写。这一类必须拦。
//
// 逐字回环比列一张字符黑名单强:黑名单会随驱动版本漂移,而"解出来的必须
// 与填进去的一致"这条判据不会。
//
// 端口另外单独判:`3306/x` 能通过回环(Addr 与 host+":"+port 逐字相等),
// 但它不是一个端口。这里挡在前面,否则运维拿到的错误是一次连接超时。
func (p BootstrapParams) MySQLDSN() (string, error) {
	host := strings.TrimSpace(p.Host)
	port := strings.TrimSpace(p.Port)
	user := strings.TrimSpace(p.User)
	name := strings.TrimSpace(p.Database)
	if host == "" || port == "" || user == "" || name == "" {
		return "", fmt.Errorf("qianye: 主机 / 端口 / 用户名 / 库名都不能为空")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", fmt.Errorf("qianye: 端口必须是 1-65535 的整数,收到 %q", port)
	}
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=true&loc=Local",
		user, p.Password, host, port, name)

	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("qianye: 连接参数拼不出合法的 DSN(%v)—— "+
			"请检查主机、用户名、库名里是否含 : @ / ? 或括号", err)
	}
	switch {
	case cfg.User != user:
		return "", fmt.Errorf("qianye: 用户名含 DSN 的分隔字符(: 或 @),解析后变成了 %q", cfg.User)
	case cfg.Passwd != p.Password:
		return "", fmt.Errorf("qianye: 密码解析后与输入不一致,请改用不含 DSN 分隔字符的密码")
	case cfg.DBName != name:
		return "", fmt.Errorf("qianye: 库名含 DSN 的分隔字符(/ 或 ?),解析后变成了 %q —— "+
			"库名里的 ? 会静默改掉整个连接的参数", cfg.DBName)
	case cfg.Addr != host+":"+port:
		return "", fmt.Errorf("qianye: 主机含 DSN 的分隔字符,解析后地址变成了 %q", cfg.Addr)
	}
	return dsn, nil
}

// NewAESKey 生成一把 base64 编码的 32 字节 AES-256 密钥。
func NewAESKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("qianye: 生成密钥失败: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// ValidateBytes 用与进程启动完全相同的判据检查一份配置字节。
func ValidateBytes(raw []byte) error {
	_, err := parseBytes(raw)
	return err
}

// RenderBootstrap 生成第一份配置并自检。自检不过时返回错误且不产出任何字节。
func RenderBootstrap(p BootstrapParams) ([]byte, error) {
	dsn, err := p.MySQLDSN()
	if err != nil {
		return nil, err
	}
	mallKey, err := NewAESKey()
	if err != nil {
		return nil, err
	}
	lotteryKey, err := NewAESKey()
	if err != nil {
		return nil, err
	}
	raw := []byte(fmt.Sprintf(bootstrapTemplate, dsn, mallKey, lotteryKey))
	if err := ValidateBytes(raw); err != nil {
		// 这是本包自己的模板出了问题,不是运维填错了。措辞必须说清楚,
		// 否则运维会去反复改那几个输入框。
		return nil, fmt.Errorf("qianye: 生成的配置未通过自检(这是程序缺陷,不是你的输入有误): %w", err)
	}
	return raw, nil
}

// BootstrapTargetPath 决定第一份配置写到哪里。
//
// 与 resolvePath 的查找顺序必须对齐,否则会出现"写成功了、重启却说没找到配置"。
//
//   - QIANYE_CONFIG 显式指定 → 就写那里(运维已经表达过意愿)
//   - 存在 ./data/ 目录 → ./data/qianye.yaml(本地开发形态,/data/ 已被 gitignore)
//   - 否则 → ./qianye.yaml
//
// 第三条正是 Docker:容器 WORKDIR=/data 且宿主 ./data 挂在这里,容器内没有
// ./data/ 子目录,于是落到 ./qianye.yaml,在宿主机上就是 ./data/qianye.yaml。
func BootstrapTargetPath() string {
	if p := common.GetEnvOrDefaultString(EnvConfigPath, ""); p != "" {
		return p
	}
	if st, err := os.Stat("./data"); err == nil && st.IsDir() {
		return "./data/qianye.yaml"
	}
	return "./qianye.yaml"
}

// WriteBootstrap 把配置写到 BootstrapTargetPath,权限 0600。
//
// 已存在则拒绝覆盖:文件里有数据库密码与密钥,而"向导又跑了一遍"绝不该是
// 丢掉一把已经加密过数据的钥匙的理由。
func WriteBootstrap(raw []byte) (string, error) {
	path := BootstrapTargetPath()
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("qianye: 配置文件已存在(%s),向导不覆盖它 —— "+
			"文件中含数据库密码与加密密钥,覆盖会让已加密的数据永久无法解开。"+
			"要重新配置请手工编辑该文件", path)
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("qianye: 创建配置目录失败: %w", err)
		}
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("qianye: 写入配置文件失败: %w", err)
	}
	return path, nil
}

// bootstrapTemplate 的三个 %s 依次是:扩展库 DSN、商城密钥、抽奖密钥。
//
// 注释保持精简 —— 完整说明书在 qianye/config/qianye.example.yaml,
// 这里每多一行都是一行将来会与说明书漂移的副本。
const bootstrapTemplate = `# 千夜扩展配置 —— 由引导部署向导生成
#
# 本文件含数据库密码与加密密钥,权限已设为 0600,请勿提交到版本库。
# 完整的配置说明(每一项的含义与代价)见 qianye/config/qianye.example.yaml。
#
# 当前状态:地基已接通,全部业务模块处于关闭状态。
# 逐个开启的办法:把下面对应段的 enabled 改成 true 并补齐该段所需的配置项。
# 模块开关支持热载 —— 把 runtime.config_reload_seconds 改成大于 0 即可,
# 改完不需要重启。**但 database 段永远不热载**,换库必须重启进程。

enabled: true

# ---------------------------------------------------------------------------
# 扩展库。全部表带 qy_ 前缀,与主库物理隔离。
# 备份纪律见 qianye/docs/deploy-database.md:它与主库必须成对做 PITR。
# ---------------------------------------------------------------------------
database:
  dsn: "%s"
  max_idle_conns: 20
  max_open_conns: 100
  conn_max_lifetime_seconds: 600
  conn_max_idle_time_seconds: 120
  connect_timeout_seconds: 5
  read_timeout_seconds: 30
  write_timeout_seconds: 30
  slow_threshold_ms: 200
  log_level: warn
  auto_migrate: true

# 台账库(AI 审核明细)。留空 = 不分家,那些表跟着上面的扩展库走。
# 什么时候值得分出去,见 qianye/docs/deploy-database.md 第 5 节。

runtime:
  # 扩展库故障时 relay 热路径一律放行。不要改成 false ——
  # 那会让扩展成为主业务的单点故障。
  hot_path_fail_open: true
  hot_path_timeout_ms: 200
  hot_async_timeout_ms: 3000
  cold_path_timeout_ms: 3000
  health_interval_seconds: 15
  breaker_failure_threshold: 5
  breaker_open_seconds: 30
  background_enabled: true
  lease_ttl_seconds: 60
  lease_renew_seconds: 20
  config_reload_seconds: 0
  hot_hook_queue_size: 4096
  hot_hook_workers: 2

two_phase:
  main_outbox_enabled: true
  compensate_interval_seconds: 30
  pending_grace_seconds: 60
  max_probe_attempts: 10
  batch_size: 200
  manual_review_after_seconds: 900
  outbox_retention_days: 30

audit:
  enabled: true
  record_ip: true
  snapshot_max_bytes: 4096
  request_enabled: true
  retention_days: 0

# ===========================================================================
# 业务模块 —— 全部显式关闭。
#
# 显式写出 enabled: false 而不是整段省略,是为了不触发启动告警:
# 「缺段」与「显式关掉」在进程内是同一个字节,而前者已经造成过三次
# "代码都编译进去了,刷新却看不到功能"的事故。
# ===========================================================================

transfer:
  enabled: false

invite:
  enabled: false

commission:
  enabled: false

ticket:
  enabled: false

availability:
  enabled: false

violation:
  enabled: false
  # 这两个才是真正决定"抓不抓"的开关,只开 enabled 是零命中。
  precheck_enabled: false
  post_charge_enabled: false

lottery:
  enabled: false
  # base64 的 32 字节 AES-256 密钥,已随本文件生成。开启抽奖时直接可用。
  # 轮换时把旧钥搬进 prize_secret_keys_retired 并把版本号抬大。
  prize_secret_key: "%s"
  prize_secret_key_version: 1

mall:
  enabled: false
  # 同上,商城用。
  secret_key: "%s"
  secret_key_version: 1
`
