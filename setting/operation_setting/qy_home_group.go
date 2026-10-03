package operation_setting

import (
	"fmt"
	"strings"
	"unicode"
)

// 首屏群聊卡片的两个选项。
//
// 定制落地页(web/src/features/qy/landing)首屏右侧默认是模型信号板;这两项
// 任意配了一项,那一格就换成群聊二维码卡。两项皆空(或值被判非法)时回落信号板,
// 所以全新装的站不会开天窗。
//
// 【命名口径 —— 本 fork 第一个自建的数据库选项,后来者照这个写】
//   - `Qy` 前缀:options 表没有白名单,任何键都能写进去。加前缀保证永远不会和
//     上游日后新增的键撞名(撞名即互相覆盖)。
//   - 扁平 CamelCase,不带点号:带点的键会先进 model 的 handleConfigUpdate 按
//     `configName.configKey` 解析,而前端 react-hook-form 会把点当成嵌套路径。
//     两个单行文本不值得为此建嵌套对象。
//   - 结尾不用 Key / Token / Secret:controller.GetOptions 按这几个后缀整条剔除
//     不下发,叫 `...QrKey` 的后果是设置页永远显示空、保存却把空值写回去,
//     而 /api/status 那边照样公开着旧值。
//   - 结尾不用 Enabled / Permission:那两个后缀在 model 里各有一个类型分支。
const (
	QyHomeGroupJoinUrlOptionKey = "QyHomeGroupJoinUrl"
	QyHomeGroupNumberOptionKey  = "QyHomeGroupNumber"
)

// qyHomeGroupJoinUrlMaxLen 是入群链接的长度上界,**这是安全项不是装饰**。
//
// 前端用 qrcode.react 把这个值编成二维码,而它在超出容量时抛的是渲染期
// RangeError("Data too long");路由根挂着 errorComponent,于是一个选项值能把
// 整个前端外壳换成通用错误页,对每一个匿名访客都如此。把上界钉在容量之下,
// 这条退化路径在存在性上就没了。
const qyHomeGroupJoinUrlMaxLen = 512

// ValidateQyHomeGroupJoinUrl 校验入群链接。空值放行 —— 清空等于关掉这块卡片,
// 挡住空值就等于管理员再也删不掉一个填错的地址。
//
// 判据刻意**不**照抄 service.ValidateTaskArtifactBaseURL:那一份额外禁掉 query
// 与 fragment,而群聊邀请链接形如 https://qm.qq.com/q/xxxx?k=... 必然带 query,
// 照那份写会把正常链接判死。
//
// 后端从不去 fetch 这个地址,只是原样放进 JSON,所以当前 SSRF 面为 0。
// **唯一会引入 SSRF 的后续动作**是「后台加二维码预览或可达性校验,由服务端去抓
// 这个地址」—— 真要做必须先过内网地址过滤,不能直接 http.Get。
func ValidateQyHomeGroupJoinUrl(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > qyHomeGroupJoinUrlMaxLen {
		return fmt.Errorf("入群链接过长(最多 %d 个字符)", qyHomeGroupJoinUrlMaxLen)
	}
	for _, r := range trimmed {
		// url.Parse 拒掉 tab / LF / NUL 这类 ASCII 控制符,却**放行**双向排版符
		// (U+202E 那一组)。这里补齐的理由很窄,别照错理由扩大它:卡片上永远
		// 不打印 URL 本身,所以页面内没有视觉欺骗面;能被欺骗的是扫码器自己弹的
		// 「是否打开此链接」确认框。
		if unicode.IsControl(r) || isBidiControl(r) {
			return fmt.Errorf("入群链接含不可见控制字符,请重新复制")
		}
	}
	if !IsSafeExternalLinkURL(trimmed) {
		return fmt.Errorf("入群链接必须以 http:// 或 https:// 开头")
	}
	return nil
}

func isBidiControl(r rune) bool {
	switch r {
	case '‎', '‏', '‪', '‫', '‬', '‭', '‮',
		'⁦', '⁧', '⁨', '⁩':
		return true
	}
	return false
}

// ValidateQyHomeGroupNumber 校验群号。空值同样放行。
//
// 字符集收在纯拉丁 ASCII 的数字、字母与 . _ - 三个符号上,长度 3-32。
// 三条理由:
//  1. 这个值原样印在未登录可见的页面上。这个集合在**构造上**装不下任何注入载荷
//     (没有协议头、没有尖括号、没有双向控制符、没有同形字),所以它不需要
//     另一套控制符判据 —— 白名单本身就是那套判据的超集。
//  2. 不含空格,于是「把一整段文案塞进公开 payload」这件事从源头消失。
//  3. 卡片上那一格走等宽轴,而等宽轴只给纯拉丁字串(中文会掉进 Courier New →
//     SimSun,字形不受控)。
//
// 比「只允许数字」宽,是为了让微信群 ID、Telegram 用户名这类非数字标识也填得进来;
// 再放宽到允许中日韩字符之前,先回头看第 3 条。
func ValidateQyHomeGroupNumber(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) < 3 || len(trimmed) > 32 {
		return fmt.Errorf("群号长度只能是 3-32 个字符")
	}
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z',
			c == '.', c == '_', c == '-':
		default:
			return fmt.Errorf("群号只能包含数字、英文字母和 . _ - ,群名称或整句说明请不要填在这里")
		}
	}
	return nil
}

// SanitizedQyHomeGroupJoinUrl / SanitizedQyHomeGroupNumber 是**读取侧**的同一道闸。
//
// 写入侧已经在落库前挡过一次,这里再挡一次不是重复劳动:updateOptionMap 的第一句
// 是无条件写 map,而装载与定期同步会把库里每一行原样重放进来。手改库、恢复备份、
// 或者日后某条不走 UpdateOption 的写入,都能让坏值径直进 OptionMap → /api/status
// → 匿名访客的 <a href>。本仓演示库里 AudioCompletionRatio 的值是字符串 "<nil>",
// 就是这么来的。
func SanitizedQyHomeGroupJoinUrl(value string) string {
	trimmed := strings.TrimSpace(value)
	if ValidateQyHomeGroupJoinUrl(trimmed) != nil {
		return ""
	}
	return trimmed
}

func SanitizedQyHomeGroupNumber(value string) string {
	trimmed := strings.TrimSpace(value)
	if ValidateQyHomeGroupNumber(trimmed) != nil {
		return ""
	}
	return trimmed
}
