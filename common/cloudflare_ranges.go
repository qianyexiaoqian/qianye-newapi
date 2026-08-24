package common

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
)

// cloudflare_ranges.go —— `TRUSTED_PROXIES=cloudflare` 这一档的网段来源。
//
// # 为什么是内置快照 + 可选文件覆盖,而不是启动时联网拉取
//
// Cloudflare 在 https://www.cloudflare.com/ips-v4 / ips-v6 上发布边缘网段。
// 「启动时去拉一次」看起来更新鲜,但它把一条**安全判据的取值**接到了一条
// 启动期网络请求上:
//
//   - 拉取失败(DNS 挂了、出网被墙、边车还没起来)就变成一个新的启动失败模式,
//     或者更糟 —— 静默退回空列表,于是 CF 后面的站点全站客户端 IP 变成 CF 边缘节点。
//   - 拉取成功但内容被篡改(DNS 投毒、企业中间人 CA、透明代理)会**扩大**信任面。
//     信任面扩大到攻击者自己的地址,他就能对任意令牌伪造 allow_ips 通过的来源 IP。
//     这是一条 TOFU 通道:第一次拉到什么就信什么,而且每次重启都重新赌一遍。
//   - CF 的网段年级别稳定(v4 列表多年未变)。为一个年变一次的清单换来一条
//     每次启动都要赌的通道,不划算。
//
// 因此这里的取法是:
//
//	内置快照                默认值。列表见 cloudflareSnapshotV4 / V6,
//	                        带取得日期。它落后于 CF 的后果是**兼容性**下降
//	                        (新网段的边缘节点不被信任 → 那部分流量的客户端 IP
//	                        退化成 CF 边缘地址),而不是安全性下降 —— 失效方向是保守的。
//	TRUSTED_PROXIES_CLOUDFLARE_FILE  运维自己 cron 一条 curl 落地成文件,
//	                        进程启动时读它。更新周期由运维决定,不引入出网依赖,
//	                        内容还要过下面的 validateCloudflarePrefixes。
//
// 文件里的内容同样不是全信:validateCloudflarePrefixes 会拒绝任何包含私网 /
// 回环 / 链路本地 / 组播 / 过宽前缀的清单。一个被写坏(或被恶意替换)成
// `0.0.0.0/0` 的文件不会让整个互联网变成可信代理,而是让服务**起不来** ——
// 这正是这类配置该有的失败方向。

// cloudflareSnapshotV4 快照取自 https://www.cloudflare.com/ips-v4(2026-08)。
//
// 更新方式:把新内容写进 TRUSTED_PROXIES_CLOUDFLARE_FILE 指向的文件,
// 或者改这里并重新构建。两种都不需要改任何逻辑。
var cloudflareSnapshotV4 = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
}

// cloudflareSnapshotV6 快照取自 https://www.cloudflare.com/ips-v6(2026-08)。
var cloudflareSnapshotV6 = []string{
	"2400:cb00::/32",
	"2606:4700::/32",
	"2803:f800::/32",
	"2405:b500::/32",
	"2405:8100::/32",
	"2a06:98c0::/29",
	"2c0f:f248::/32",
}

// CloudflareSnapshotSource 是内置快照的出处,诊断页展示用。
const CloudflareSnapshotSource = "built-in snapshot of https://www.cloudflare.com/ips-v4 + ips-v6 (2026-08)"

// CloudflarePrefixes 返回 `cloudflare` 档要信任的网段。
func CloudflarePrefixes() ([]string, error) {
	path := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES_CLOUDFLARE_FILE"))
	if path == "" {
		return append(append([]string{}, cloudflareSnapshotV4...), cloudflareSnapshotV6...), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES_CLOUDFLARE_FILE %q: %w", path, err)
	}
	prefixes, err := ParseCloudflarePrefixFile(string(data))
	if err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXIES_CLOUDFLARE_FILE %q: %w", path, err)
	}
	return prefixes, nil
}

// ParseCloudflarePrefixFile 解析 CF 网段清单文件。
//
// 格式就是 `curl https://www.cloudflare.com/ips-v4` 的原样输出:一行一个 CIDR。
// 额外允许空行与 `#` 注释,方便把 v4 与 v6 拼进同一个文件并写上取得日期。
func ParseCloudflarePrefixFile(content string) ([]string, error) {
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := validateCloudflarePrefixes(out); err != nil {
		return nil, err
	}
	return out, nil
}

// cloudflarePrefixCountCap 一份清单最多多少条。CF 的两份清单加起来 22 条,
// 上限留得很宽只是为了挡住「文件被换成了一份几万行的东西」。
const cloudflarePrefixCountCap = 512

// validateCloudflarePrefixes 校验一份外部提供的 CDN 网段清单。
//
// 它守的是「这个文件被写坏或被换掉之后,信任面不会被悄悄放宽」:
// 拒绝私网 / 回环 / 链路本地 / 组播 / 未指定地址,以及过宽的前缀。
// 任何一条不合格就整份拒绝并让启动失败 —— 部分接受会得到一个"看起来在跑、
// 实际信任面已经变了"的进程,那是最坏的一种结局。
func validateCloudflarePrefixes(prefixes []string) error {
	if len(prefixes) == 0 {
		return errors.New("cloudflare range list is empty")
	}
	if len(prefixes) > cloudflarePrefixCountCap {
		return fmt.Errorf("cloudflare range list has %d entries, more than the %d allowed",
			len(prefixes), cloudflarePrefixCountCap)
	}
	for _, raw := range prefixes {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("invalid cloudflare range %q: %w", raw, err)
		}
		addr := prefix.Masked().Addr()
		switch {
		case addr.IsLoopback():
			return fmt.Errorf("cloudflare range %q is loopback space", raw)
		case addr.IsPrivate():
			return fmt.Errorf("cloudflare range %q is private space", raw)
		case addr.IsLinkLocalUnicast(), addr.IsLinkLocalMulticast():
			return fmt.Errorf("cloudflare range %q is link-local space", raw)
		case addr.IsMulticast():
			return fmt.Errorf("cloudflare range %q is multicast space", raw)
		case addr.IsUnspecified():
			return fmt.Errorf("cloudflare range %q is the unspecified address", raw)
		}
		// 过宽的前缀。CF 实际发布的最宽是 v4 /13 与 v6 /29;这里留出余量,
		// 但仍然挡得住 0.0.0.0/0、10/8 级别的"一条顶一片"。
		if addr.Is4() && prefix.Bits() < 12 {
			return fmt.Errorf("cloudflare range %q is wider than /12", raw)
		}
		if !addr.Is4() && prefix.Bits() < 24 {
			return fmt.Errorf("cloudflare range %q is wider than /24", raw)
		}
	}
	return nil
}
