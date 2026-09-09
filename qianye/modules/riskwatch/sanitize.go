package riskwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// sanitize.go —— 落库之前对上下文做的三件事。
//
//	1. 剥离内联二进制   一张 base64 图片就是 10 MB,一条都不能进库
//	2. 抹掉凭证         bearer / sk- 这类东西留在取证库里是纯负债
//	3. 按字符上限截断   掐头去尾,保留两端
//
// # 为什么不复用违规检测那套脱敏
//
// 那一套还会抹掉邮箱、手机号、身份证、银行卡。它的场景是**全站自动归档**:
// 管理员研判一条自动命中的违规,不需要知道用户的手机号,而平台留着它就要承担
// 个人数据的全部责任。
//
// 这里的场景相反 —— 一次由管理员**指名立案**的定向取证,而个人标识符常常
// 就是证据本身:批量生成诈骗短信、往一串手机号里灌营销文案、拿别人的身份证号
// 编材料,把这些抹掉之后留下的记录恰好证明不了任何东西。
//
// 凭证是唯一的例外,而且方向相反:一把被用户粘进提示词的 API key 对研判毫无
// 价值,却会让这个取证库变成一个凭证仓库。所以它照抹。
//
// 也就是说这不是"同一份口径的第二份实现",而是**两条不同的策略**,各自服务于
// 一个不同的场景。做成一份可配的通用脱敏器是错的:那会让"抹不抹手机号"变成
// 一个开关,而它其实是这两个功能各自定义的一部分。

var (
	// dataURIRe 匹配 data:<mime>;base64,<payload> 形式的内联附件。
	dataURIRe = regexp.MustCompile(`data:([a-zA-Z0-9.+-]+/[a-zA-Z0-9.+-]+)?;base64,[A-Za-z0-9+/=]{64,}`)
	// longB64Re 匹配裸的长 base64 串。512 是"疑似内联二进制"的判定长度:
	// 正常文本里出现 512 个连续 base64 字符的概率可以忽略,而 512 字符的图片
	// 是不存在的 —— 误伤与漏网两侧都足够安全。
	longB64Re = regexp.MustCompile(`[A-Za-z0-9+/]{512,}={0,2}`)

	// credentialRes 是凭证的判据。三条都要求足够长的随机段,因此不会误伤
	// "bearer 是什么意思"这类正常句子。
	credentialRes = []*regexp.Regexp{
		regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{16,}`),
		regexp.MustCompile(`\b(sk-[A-Za-z0-9_\-]{16,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,})\b`),
		regexp.MustCompile(`(?i)\b(api[_\-]?key|secret|token)\s*[:=]\s*["']?[A-Za-z0-9._\-]{24,}["']?`),
	}
)

// maxCaptureFiles 是单条记录最多描述的多模态文件数,超出只记数量。
const maxCaptureFiles = 32

// fileDescriptor 描述一个多模态输入,不含任何二进制。
//
// SHA256 是这套设计里最有价值的字段:同一张图被多个账号反复上传时,可以按
// 哈希把这些账号串起来,而完全不必保存图片本体。
type fileDescriptor struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"`
	Origin string `json:"origin"` // url | base64
	URL    string `json:"url,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
}

// sanitized 是一段文本过完三道工序之后的结果。
type sanitized struct {
	Text string
	// Chars 是**截断之前**的字符数。它与 len(Text) 不同:只看后者分不出
	// "这个人就问了三个字"与"上限设得太小"。
	Chars     int
	Truncated bool
}

// sanitizeText 依次执行剥离、抹凭证、截断,顺序不可换。
//
// 截断必须排在**最后**:反过来的话,被截掉的那一半从未参与替换,而内联二进制
// 恰恰是最占位置的一段 —— 先截的结果是一整条记录被一张图占满,正文一个字都没留下。
func sanitizeText(s string, maxChars int) sanitized {
	if s == "" {
		return sanitized{}
	}
	s = stripInlineBinary(s)
	for _, re := range credentialRes {
		s = re.ReplaceAllString(s, "«credential»")
	}
	chars := utf8.RuneCountInString(s)
	if maxChars <= 0 || chars <= maxChars {
		return sanitized{Text: s, Chars: chars}
	}
	return sanitized{Text: clipHeadTail(s, maxChars), Chars: chars, Truncated: true}
}

// stripInlineBinary 把内联二进制替换成一个描述符占位。
//
// 保留 MIME、长度与短哈希:一条"这里原本有一张 2.1 MB 的 image/png"远比一条
// 什么都没有的空白有用 —— 尤其是当同一个哈希在另一个账号的记录里也出现时。
func stripInlineBinary(s string) string {
	out := dataURIRe.ReplaceAllStringFunc(s, func(m string) string {
		mime := ""
		if g := dataURIRe.FindStringSubmatch(m); len(g) > 1 {
			mime = g[1]
		}
		idx := strings.Index(m, "base64,")
		return descriptorFor(mime, m[idx+len("base64,"):])
	})
	return longB64Re.ReplaceAllStringFunc(out, func(m string) string {
		return descriptorFor("", m)
	})
}

func descriptorFor(mime, payload string) string {
	if mime == "" {
		mime = "application/octet-stream"
	}
	return fmt.Sprintf("«%s,%dB,sha256:%s»", mime, len(payload), shortHash(payload))
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

// clipHeadTail 掐头去尾保留两端,按 rune 计。
//
// 不是截尾:一次刷接口的提示词,前面是指令、后面是载荷,而中间往往是大段
// 重复的样例。截尾会把载荷整段丢掉,而那正是要看的东西。
//
// 按 rune 而不是 byte:配置项叫 capture_max_chars,而中文一个字三个字节 ——
// 按字节截会在管理员填 4000 时只留下一千多字,而且可能把一个字截成半个。
func clipHeadTail(s string, maxChars int) string {
	runes := []rune(s)
	if maxChars <= 0 || len(runes) <= maxChars {
		return s
	}
	const marker = "\n…«已截断»…\n"
	// 上限太小时没有分两段的意义,直接留头部。
	if maxChars <= len([]rune(marker))+8 {
		return string(runes[:maxChars])
	}
	budget := maxChars - len([]rune(marker))
	head := budget / 2
	tail := budget - head
	return string(runes[:head]) + marker + string(runes[len(runes)-tail:])
}

// describeFiles 把 meta.Files 转成描述符 JSON 数组。空数组返回空串。
func describeFiles(files []*types.FileMeta) string {
	if len(files) == 0 {
		return ""
	}
	out := make([]fileDescriptor, 0, len(files))
	for i, f := range files {
		if i >= maxCaptureFiles {
			break
		}
		if f == nil || f.Source == nil {
			continue
		}
		raw := f.Source.GetRawData()
		d := fileDescriptor{
			Ref:   fmt.Sprintf("f%d", i),
			Kind:  string(f.FileType),
			Bytes: int64(len(raw)),
		}
		if f.Source.IsURL() {
			d.Origin = "url"
			// 剥掉 query:签名 URL 的 query 里常带临时凭证,那是不该落库的。
			d.URL = truncate(stripQuery(raw), 512)
		} else {
			d.Origin = "base64"
			d.MIME = mimeOfDataURI(raw)
			// 哈希的是 base64 文本本身而不是解码后的字节:同一份内容的 base64
			// 表示是稳定的,而解码 1 MB 只为了换个哈希值不划算。
			d.SHA256 = shortHash(raw)
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return ""
	}
	b, err := common.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

func stripQuery(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func mimeOfDataURI(s string) string {
	if !strings.HasPrefix(s, "data:") {
		return ""
	}
	rest := s[len("data:"):]
	if i := strings.IndexByte(rest, ';'); i >= 0 {
		return rest[:i]
	}
	return ""
}

// truncate 按**字节**截断到列宽,并保证不切开一个多字节字符。
//
// 与 clipHeadTail 的分工:那一个服务于正文的可读性(按 rune、掐头去尾),
// 这一个服务于 varchar 列宽(按 byte、直接截尾)。混用会让一个 64 字节的
// varchar 列在存 21 个以上的中文用户名时插入失败。
func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}
