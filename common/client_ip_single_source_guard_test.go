package common

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// client_ip_single_source_guard_test.go —— 「全站只有一处取客户端 IP」的机器校验。
//
// # 它在守什么
//
// 客户端 IP 是四类判据的取值来源:令牌 allow_ips、按 IP 的限流、审计与资金台账、
// 风控去重。这四类必须看到同一个字符串,否则会出现「限流按真实 IP、台账记伪造
// IP」这种自相矛盾 —— 两边都不报错,只有真出事的时候才发现取证数据是假的。
//
// 统一之前,仓库里有 90 处直接调 gin 的 c.ClientIP()。它们今天恰好一致,是因为
// 它们调的是同一个函数;而只要有一处开始走别的取法(或者 gin 的实现在某次升级里
// 变了语义),漂移就是静默的。这条断言把"只有一处"从约定变成机器校验。
//
// # 为什么是文本扫描而不是 AST
//
// 被守的东西是一个**字面调用**:`x.ClientIP()`。AST 在这里不会更准(要判断
// receiver 是不是 *gin.Context 就得做类型检查,而那需要 go/packages 加载整个仓库),
// 反而会让这条守卫本身变成一个会坏的东西。文本扫描的漏报面是"有人把它包一层
// 再调" —— 那种写法本身就会在 review 里显形。
//
// # 例外
//
// 只有 common/client_ip.go 自己。它连 gin 的 ClientIP() 都不调(gin 的实现有
// 四处在真实部署里会出错,见该文件头),所以这份清单里的例外都是**注释**里
// 提到这个名字的文件,不是真的调用点。

// clientIPCallSiteExemptions 是允许出现 `.ClientIP()` 字样的文件(仓库相对路径)。
//
// 每一条都要写清理由。没有理由的例外等于把守卫关掉:下一个人会照着它再加一条。
var clientIPCallSiteExemptions = map[string]string{
	"common/client_ip.go":                          "全站唯一的实现本身;文件里出现的 c.ClientIP() 全在注释里,讲的正是「为什么不用它」。",
	"common/client_ip_single_source_guard_test.go": "这份守卫自己。",
}

// clientIPGuardSkipDirs 是遍历时跳过的目录。
var clientIPGuardSkipDirs = map[string]bool{
	".git":         true,
	".claude":      true,
	"node_modules": true,
	"web":          true,
	"docs":         true,
}

func TestClientIPHasASingleCallSite(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)

	var offenders []string
	scanned := 0
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if clientIPGuardSkipDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		scanned++
		if _, exempt := clientIPCallSiteExemptions[relative]; exempt {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(content), "\n") {
			// 纯注释行不算调用点。这个名字会出现在讲「为什么不用 gin 那个」的
			// 文档注释里,把它们也算成违规等于逼人把解释删掉 —— 而解释正是
			// 这条守卫希望留下的东西。
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, ".ClientIP()") {
				offenders = append(offenders, relative+":"+strconv.Itoa(i+1)+" "+strings.TrimSpace(line))
				continue
			}
			// gin 的那个值还有第二个入口,而它**不带括号**,所以上面那条字面量
			// 扫描天然看不见:gin.LoggerWithFormatter 的 LogFormatterParams.ClientIP
			// 字段就是 c.ClientIP()(gin@v1.9.1 logger.go:254)。
			// 访问日志因此曾经与审计台账在同一条请求上给出两个不同的来源 IP,
			// 而日志那一侧是攻击者可控的 —— 实测四种拓扑下分叉(多值 XFF、
			// 链上带端口、CLIENT_IP_HEADERS 生效、::ffff:/大小写归一化)。
			// 「全站唯一取法」这句话必须把这个字段也算进去。
			if strings.Contains(line, "param.ClientIP") ||
				strings.Contains(line, "Params.ClientIP") {
				offenders = append(offenders, relative+":"+strconv.Itoa(i+1)+" "+strings.TrimSpace(line))
			}
		}
		return nil
	}))

	// 自检:遍历器被改坏(跳过了半个仓库)时这条会先红,而不是让上面那条
	// 静默全绿。此刻实测扫到 1000 上下的 .go 文件。
	assert.Greater(t, scanned, 600,
		"扫到的 Go 文件太少,遍历器八成被改坏了 —— 那样上面那条断言就是假绿")

	assert.Empty(t, offenders,
		"客户端 IP 只能有一处取法(common.ClientIP)。gin 的 c.ClientIP() 在多值请求头、"+
			"IPv4-mapped 归一化、带端口条目与厂商头四处会给出不同的答案,"+
			"混用会造成「限流按真实 IP、台账记伪造 IP」——两边都不报错。"+
			"新代码请调 common.ClientIP(c);确有理由的例外写进 clientIPCallSiteExemptions 并说明。")
}
