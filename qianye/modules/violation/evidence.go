package violation

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/qianye/config"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// 证据留存的体积账(1000 次违规/天):
//
//	纯元数据                       ~1 KB/条  →   30 MB/月   可接受
//	归一化文本(截断后 8 KB)       ~3 KB/条  →   90 MB/月   可接受
//	原始 body 含 base64 图片       ~10 MB/条 →  300 GB/月   不可接受
//	body 上限场景(128 MB)         128 MB/条 →       ——     绝对不可接受
//
// 所以硬性结论有三条,缺一条都会在上线两周后炸掉磁盘:
//  1. base64 二进制一律不入库,只留描述符(MIME/字节数/SHA256);
//  2. 归档的是归一化上下文而不是原始 body —— 既小,也更适合人看;
//  3. 三层闸门:剥离 → 按 evidence_max_bytes 截断 → gzip 压缩。
const (
	// maxEvidenceFiles 是单条记录最多描述的多模态文件数,超出只记数量。
	maxEvidenceFiles = 32
	// minBase64Run 是"疑似内联二进制"的判定长度。取 512 而不是更小:
	// 正常文本里出现 512 个连续 base64 字符的概率可以忽略,而 512 字符的
	// 图片是不存在的,误伤与漏网两侧都足够安全。
	minBase64Run = 512
	// maxStoredBytes 是压缩后 blob 的硬上限。MySQL 5.7 的 max_allowed_packet
	// 默认只有 4MB,不少云 RDS 同样保守,压缩后 1MB 在任何常见配置下都安全。
	maxStoredBytes = 1 << 20
)

// fileDescriptor 描述一个多模态输入,不含任何二进制。
//
// SHA256 是这套设计里最有价值的字段:同一张违规图被多个账号反复上传时,
// 可以按哈希做识别与封禁,而完全不必保存图片本体(CSAM 类违规的图片留存
// 本身就可能违法)。
type fileDescriptor struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"`
	Origin string `json:"origin"` // url | base64
	URL    string `json:"url,omitempty"`
	MIME   string `json:"mime,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// evidenceDoc 是归档进 payload.Body 的归一化上下文。
type evidenceDoc struct {
	V    int            `json:"v"`
	Meta map[string]any `json:"meta"`
	Text string         `json:"text"`
	Hit  map[string]any `json:"hit,omitempty"`
	Up   map[string]any `json:"upstream,omitempty"`
}

// buildEvidence 组装并压缩一条证据。
//
// 必须在 relay 线程里同步调用:body 存储会在 c.Next() 返回后被释放,
// 异步 goroutine 再去读会拿到已释放的存储。"取数据"同步、"落库"异步。
func buildEvidence(rec *Record, in scanInput, v *verdict, files []*types.FileMeta) *Payload {
	maxRaw := config.Get().Violation.EvidenceMaxBytes
	if maxRaw <= 0 {
		maxRaw = 8192
	}

	origin := int64(len(in.Text))
	text, stripped := stripInlineBinary(in.Text)
	text, stats := redact(text)
	truncated := len(text) > maxRaw
	text = clipHeadTail(text, maxRaw)

	doc := evidenceDoc{
		V: 1,
		Meta: map[string]any{
			"request_id":   rec.RequestId,
			"model":        rec.ModelName,
			"group":        rec.UsingGroup,
			"relay_format": rec.RelayFormat,
			"phase":        rec.Phase,
			"created_at":   rec.CreatedAt,
			"stripped_b64": stripped,
		},
		Text: text,
	}
	if v != nil && v.Rule != nil {
		doc.Hit = map[string]any{
			"rule_id": v.Rule.R.Id,
			"terms":   v.Terms,
			"snippet": v.Snippet,
		}
	}
	if in.ErrCode != "" || in.StatusCode != 0 || in.RejectReason != "" {
		doc.Up = map[string]any{
			"status_code":   in.StatusCode,
			"error_code":    in.ErrCode,
			"error_message": clipHeadTail(in.UpstreamText, 2048),
			"reject_reason": in.RejectReason,
		}
	}

	raw, err := common.Marshal(doc)
	if err != nil {
		return nil
	}
	blob, err := gzipBytes(raw)
	if err != nil {
		return nil
	}
	if len(blob) > maxStoredBytes {
		// 压缩后仍超限只可能是证据本身异常(例如高熵二进制没被识别出来),
		// 与其写坏一行,不如放弃 payload —— 主表的命中片段仍在。
		return nil
	}

	return &Payload{
		Codec:        "gzip",
		OriginBytes:  origin,
		RawBytes:     int64(len(raw)),
		StoredBytes:  int64(len(blob)),
		Truncated:    truncated,
		Body:         blob,
		Redacted:     len(stats) > 0,
		RedactStats:  mapToJSON(stats),
		FilesSummary: describeFiles(files),
		CreatedAt:    rec.CreatedAt,
	}
}

// decodeEvidence 还原归档文本,供管理端查看。
func decodeEvidence(p *Payload) (string, error) {
	if p == nil || len(p.Body) == 0 {
		return "", nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(p.Body))
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var buf bytes.Buffer
	// 解压上限与写入上限一致:压缩包是我们自己写的,但仍要防"解压炸弹"式的脏数据。
	if _, err := buf.ReadFrom(io.LimitReader(zr, 16<<20)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ───────────────────────────── base64 剥离 ─────────────────────────────

var (
	dataURIRe = regexp.MustCompile(`data:([a-zA-Z0-9.+-]+/[a-zA-Z0-9.+-]+)?;base64,[A-Za-z0-9+/=]{64,}`)
	longB64Re = regexp.MustCompile(`[A-Za-z0-9+/]{` + fmt.Sprint(minBase64Run) + `,}={0,2}`)
)

// stripInlineBinary 把内联二进制替换成描述符。
//
// 返回被剥离的段数:它在管理端是有意义的信号 —— "这条记录原本带了 12 张图"
// 与"这条是纯文本"在研判时是完全不同的两件事。
func stripInlineBinary(s string) (string, int) {
	if s == "" {
		return s, 0
	}
	n := 0
	out := dataURIRe.ReplaceAllStringFunc(s, func(m string) string {
		n++
		mime := ""
		if g := dataURIRe.FindStringSubmatch(m); len(g) > 1 {
			mime = g[1]
		}
		idx := strings.Index(m, "base64,")
		payload := m[idx+len("base64,"):]
		return descriptorFor(mime, payload)
	})
	out = longB64Re.ReplaceAllStringFunc(out, func(m string) string {
		n++
		return descriptorFor("", m)
	})
	return out, n
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

// describeFiles 把 meta.Files 转成描述符 JSON 数组。
func describeFiles(files []*types.FileMeta) string {
	if len(files) == 0 {
		return ""
	}
	out := make([]fileDescriptor, 0, len(files))
	for i, f := range files {
		if f == nil || f.Source == nil {
			continue
		}
		if i >= maxEvidenceFiles {
			break
		}
		raw := f.Source.GetRawData()
		d := fileDescriptor{
			Ref:    fmt.Sprintf("f%d", i),
			Kind:   string(f.FileType),
			Detail: f.Detail,
			Bytes:  int64(len(raw)),
		}
		if f.Source.IsURL() {
			d.Origin = "url"
			// 剥掉 query:签名 URL 的 query 里常带临时凭证,那是不该落库的。
			d.URL = stripQuery(raw)
		} else {
			d.Origin = "base64"
			d.MIME = mimeOfDataURI(raw)
			// 哈希的是 base64 文本本身而不是解码后的字节:同一份内容的 base64
			// 表示是稳定的,而解码 1MB 只为了换个哈希值不划算。
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
		u = u[:i]
	}
	return truncate(u, 512)
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

// ───────────────────────────── 脱敏 ─────────────────────────────

// 脱敏在写入前执行,因此数据库里从来不存在未脱敏的原文。
//
// 这是刻意的取舍:归档内容是用户输入的原始文本,属于个人数据。保留原文意味着
// 平台承担 GDPR/个保法下的全部责任,而管理员研判违规并不需要用户的手机号与邮箱。
// 命中片段本身不额外保护 —— 管理员必须看到违规内容才能判断,若违规内容里恰好
// 含邮箱,邮箱同样会被替换,这是可接受的。
var redactors = []struct {
	name string
	re   *regexp.Regexp
	repl string
}{
	{"bearer", regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]{16,}`), "«bearer»"},
	{"api_key", regexp.MustCompile(`\b(sk-[A-Za-z0-9_\-]{16,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,})\b`), "«apikey»"},
	{"email", regexp.MustCompile(`[\w.+\-]+@[\w\-]+\.[\w.\-]+`), "«email»"},
	{"id_card_cn", regexp.MustCompile(`\b[1-9]\d{5}(19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`), "«idcard»"},
	{"phone_cn", regexp.MustCompile(`(?:\+?86)?1[3-9]\d{9}`), "«phone»"},
	{"bank_card", regexp.MustCompile(`\b\d{16,19}\b`), "«bankcard»"},
}

// redact 对文本执行脱敏,返回替换后的文本与每类的命中次数。
//
// 顺序有意义:id_card_cn 必须排在 bank_card 之前,否则 18 位身份证会先被
// 当成银行卡号吃掉,统计口径就错了。
func redact(s string) (string, map[string]interface{}) {
	if s == "" {
		return s, nil
	}
	stats := map[string]interface{}{}
	for _, r := range redactors {
		n := 0
		s = r.re.ReplaceAllStringFunc(s, func(string) string {
			n++
			return r.repl
		})
		if n > 0 {
			stats[r.name] = n
		}
	}
	if len(stats) == 0 {
		return s, nil
	}
	return s, stats
}

// ───────────────────────────── 压缩 ─────────────────────────────

func gzipBytes(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
