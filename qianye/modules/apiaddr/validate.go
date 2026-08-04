package apiaddr

import (
	"net/url"
	"strings"
	"unicode/utf8"
)

// 长度上限。都比对应列宽小一截,留给多字节字符 —— varchar(64) 是 64 个字符,
// 而这里量的是 rune 数,64 个中文在 utf8mb4 下是 192 字节,列宽是够的。
const (
	maxNameRunes  = 32
	maxRemarkRune = 80
	maxURLLen     = 256

	// maxAddresses 是整表行数上限。
	//
	// 它不是性能约束(几十行谁都扛得住),而是**用户侧那个选择弹窗的可用性约束**:
	// 一个需要滚三屏才能选完的地址列表,用户只会随便点第一个,那还不如不给选。
	// 同时它也让"整表重排"这件事的输入天然有界。
	maxAddresses = 30

	// sortStep 是重排时相邻两行的序号间隔。
	//
	// 留间隔而不是直接用 0,1,2,…:将来若要支持"插到某两条之间"而不重排整表,
	// 中间有空位可用。现在整表重排用不到,但代价是零。
	sortStep = 10
)

// normalizeName 校验并归一化地址名称。
func normalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errNameRequired
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return "", errNameTooLong
	}
	return name, nil
}

// normalizeRemark 校验并归一化备注。空是合法的。
func normalizeRemark(raw string) (string, error) {
	remark := strings.TrimSpace(raw)
	if utf8.RuneCountInString(remark) > maxRemarkRune {
		return "", errRemarkTooLong
	}
	return remark, nil
}

// normalizeURL 校验并归一化 API 地址。
//
// # 为什么必须在后端归一化,而不是"前端填什么存什么"
//
// 这个值会被原样拼进剪贴板里的连接信息 JSON,由用户粘进桌面客户端。
// 三件事必须在这里挡住:
//
//  1. **scheme 白名单**。`url.Parse` 对 `javascript:…` / `data:…` 一样解析成功,
//     而这个字符串在管理端列表里是要被渲染成可点链接的。只放行 http/https。
//     顺带也挡住 `//evil.com` 这种协议相对写法(Scheme 为空)。
//  2. **不许带凭据**。`https://user:pass@host` 是合法 URL,存进来就是把一份
//     密码明文放进一张会被完整下发给所有登录用户的表里。
//  3. **不许带查询串/片段**。这里要的是"服务端点",不是一次具体请求;
//     `?key=sk-…` 这种误粘贴同样是凭据泄漏,而且客户端拼路径时会拼出坏地址。
//
// 归一化只做一件事:去掉路径尾部的斜杠。客户端拼 `/v1/...` 时自带前导斜杠,
// 存着尾斜杠会拼出 `https://a.com//v1`。
func normalizeURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errURLRequired
	}
	if len(s) > maxURLLen {
		return "", errURLTooLong
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errURLInvalid
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errURLScheme
	}
	if u.User != nil {
		return "", errURLCredentials
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errURLExtra
	}
	if u.Host == "" {
		return "", errURLInvalid
	}
	path := strings.TrimRight(u.EscapedPath(), "/")
	return u.Scheme + "://" + u.Host + path, nil
}

// normalizeOrderIds 校验整表重排的入参,返回去空白后的 id 序列。
//
// 只做"这串 id 本身合不合法"(非空、不超上限、无重复);"它是否恰好等于库里
// 当前的全集"必须在事务里对着真实数据判(见 applyOrder)——那是一个并发条件,
// 在这里判等于用一份读到的旧快照去证明另一份旧快照。
func normalizeOrderIds(ids []int) ([]int, error) {
	if len(ids) == 0 || len(ids) > maxAddresses {
		return nil, errInvalidParam
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return nil, errInvalidParam
		}
		seen[id] = true
	}
	return ids, nil
}
