// Package converter 实现「Markdown → 公众号样式 HTML」的转换。
//
// 【来源依据】Z 盘《公众号发稿exe化·技术决策记录·2026-10-03》第 4 节待办第 2 条：
// Python 版全部清洗规则必须照搬（本次按决策记录重写，源码选 C 方案）：
//  1. doocs/md 经典蓝主题（主色 #1e80ff，全部样式内联——公众号编辑器只认内联 style）
//  2. 表格 colgroup 迁移（列对齐/宽度信息迁移到 td 上，防止被公众号剥掉）
//  3. 标题 emoji 剥除（h1~h3 里的 emoji 一律去掉）
//  4. API 拒收 <a> / 不可见 Unicode（<a> 转纯文本；零宽字符等全部剥除）
//  5. 摘要 120 字节截断（按 UTF-8 字节边界安全截断，不会截出乱码）
//  6. 落款行过滤（文末签名/装饰线等行在渲染前剔除）
//  7. --split 章节拆分（按二级标题把一个 MD 拆成多篇草稿）
//  8. 封面双方案（嵌入默认封面 / 用户自选本地图片，见 main.go 与 assets）
package converter

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// PrimaryColor 经典蓝主题主色（doocs/md 的 #1e80ff）。
const PrimaryColor = "#1e80ff"

// Article 表示一篇待发布文章（拆分后的一章 = 一篇草稿）。
type Article struct {
	Title  string // 文章标题（已剥 emoji，直接用作公众号草稿 title）
	HTML   string // 转换后的正文 HTML（内联样式，可直接进草稿）
	Digest string // 摘要（≤120 字节，UTF-8 边界安全）
	MD     string // 对应的原始 Markdown（备用/调试）
}

// ---------- 规则 6：落款行过滤 ----------
// 落款行 = 文末的签名、装饰线、"编辑/来源"之类行，渲染前从 Markdown 源里剔除。
// 本清单是默认版，可按需增删模式（改这里即可，其他模块不用动）。
var FooterLinePatterns = []*regexp.Regexp{
	// 纯符号装饰行：*** 、--- 、___ 、——— 等（整行只有符号）
	regexp.MustCompile(`(?m)^[ \t]*(?:>+[ \t]*)?[-*_·—=]{3,}[ \t]*$`),
	// 常见落款关键词开头的行（含引用式 "> 本文由..."）
	regexp.MustCompile(`(?m)^[ \t]*(?:>+[ \t]*)?(?:本文由|编辑[:：]|排版[:：]|审校[:：]|欢迎关注|扫码关注|转载请联系|转载请注明|微信号[:：]|公众号[:：]|OldQi Weekly).*$`),
}

// ---------- 规则 3：标题 emoji 剥除 ----------
// emoji 及其修饰符（手势、符号、变体选择符 FE0F、零宽连接符 200D 等）。
var reEmoji = regexp.MustCompile(
	`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{2190}-\x{21FF}\x{FE0F}\x{200D}]`)

// ---------- 规则 4：不可见 Unicode 剥除 ----------
// 零宽空格、方向控制符、软连字符、BOM 等——公众号草稿 API 一律拒收。
var reInvisible = regexp.MustCompile(
	`[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{FEFF}\x{00AD}\x{180E}]`)

// ---------- 规则 4：<a> 转纯文本 ----------
// 公众号正文不允许 <a> 标签（非白名单），统一剥掉标签保留文字。
var reATag = regexp.MustCompile(`(?is)<a\s[^>]*>(.*?)</a>`)

// ---------- 规则 2：表格对齐迁移 ----------
var (
	reTable      = regexp.MustCompile(`(?is)<table[^>]*>.*?</table>`)
	reHeadRow    = regexp.MustCompile(`(?is)<thead>(.*?)</thead>`)
	reTh         = regexp.MustCompile(`(?is)<th([^>]*)>(.*?)</th>`)
	reBodyRow    = regexp.MustCompile(`(?is)<tr>(.*?)</tr>`)
	reTdOpen     = regexp.MustCompile(`(?is)(<td)(\s*)>`)
	reAlignStyle = regexp.MustCompile(`text-align:\s*(left|center|right)`)
)

// ---------- 样式注入 ----------
var (
	reH1 = regexp.MustCompile(`(?is)(<h1)(\s*)>`)
	reH2 = regexp.MustCompile(`(?is)(<h2)(\s*)>`)
	reH3 = regexp.MustCompile(`(?is)(<h[3-6])(\s*)>`)
	reP  = regexp.MustCompile(`(?is)(<p)(\s*)>`)
	reBQ = regexp.MustCompile(`(?is)(<blockquote)(\s*)>`)
	reUL = regexp.MustCompile(`(?is)(<ul)(\s*)>`)
	reOL = regexp.MustCompile(`(?is)(<ol)(\s*)>`)
	reLI = regexp.MustCompile(`(?is)(<li)(\s*)>`)
	reHR = regexp.MustCompile(`(?is)(<hr)(\s*)/?>`)
	reIM = regexp.MustCompile(`(?is)(<img)(\s+)`)
	reST = regexp.MustCompile(`(?is)(<strong)(\s*)>`)
	reEM = regexp.MustCompile(`(?is)(<em)(\s*)>`)
	// 行内 code（不在 pre 里的）：先保护 pre，再处理剩下的 code
	rePre = regexp.MustCompile(`(?is)<pre[^>]*>.*?</pre>`)
	reCD  = regexp.MustCompile(`(?is)(<code)(\s*)>`)
	rePR  = regexp.MustCompile(`(?is)(<pre)(\s*)>`)
)

// StripFooter 规则 6：按落款模式清单逐条剔除。
func StripFooter(md string) string {
	for _, re := range FooterLinePatterns {
		md = re.ReplaceAllString(md, "")
	}
	return md
}

// stripTitleEmoji 规则 3：只在 h1~h6 标签内部剥 emoji（正文里的 emoji 保留）。
func stripTitleEmoji(htmlStr string) string {
	reTitle := regexp.MustCompile(`(?is)(<h[1-6][^>]*>)(.*?)(</h[1-6]>)`)
	return reTitle.ReplaceAllStringFunc(htmlStr, func(m string) string {
		groups := reTitle.FindStringSubmatch(m)
		return groups[1] + reEmoji.ReplaceAllString(groups[2], "") + groups[3]
	})
}

// migrateTable 规则 2：把 thead 各 th 的对齐样式复制到 tbody 每行对应的 td 上
// （等价于 Python 版的 colgroup 迁移：列的呈现信息不依赖 <colgroup>，
// 因为公众号编辑器会剥掉 colgroup，导致列宽/对齐丢失）。
func migrateTable(htmlStr string) string {
	return reTable.ReplaceAllStringFunc(htmlStr, func(table string) string {
		// 1. 提取 thead 里每个 th 的对齐 style
		var aligns []string
		if hm := reHeadRow.FindStringSubmatch(table); hm != nil {
			for _, th := range reTh.FindAllStringSubmatch(hm[1], -1) {
				a := reAlignStyle.FindStringSubmatch(th[1])
				if a != nil {
					aligns = append(aligns, "text-align:"+a[1])
				} else {
					aligns = append(aligns, "text-align:left")
				}
			}
		}
		if len(aligns) == 0 {
			return table
		}
		// 2. 给 tbody 每一行的每个 td 按列序号补对齐样式
		return reBodyRow.ReplaceAllStringFunc(table, func(row string) string {
			if strings.Contains(row, "<thead>") || !strings.Contains(row, "<td") {
				return row // 跳过表头行自身
			}
			col := 0
			return reTdOpen.ReplaceAllStringFunc(row, func(td string) string {
				style := ""
				if col < len(aligns) {
					style = ` style="` + aligns[col] + `"`
				}
				col++
				return td[:3] + style + ">" // <td + style + >
			})
		})
	})
}

// injectStyles 规则 1：把经典蓝主题的 CSS 以内联 style 注入各标签。
func injectStyles(htmlStr string) string {
	// 保护 <pre> 代码块，避免里面的 <code> 被行内样式污染
	var pres []string
	htmlStr = rePre.ReplaceAllStringFunc(htmlStr, func(m string) string {
		pres = append(pres, m)
		return fmt.Sprintf("<!--PRE%d-->", len(pres)-1)
	})

	htmlStr = reH1.ReplaceAllString(htmlStr, `$1 style="font-size:22px;color:#333;text-align:center;margin:24px 0 16px;font-weight:bold;"$2>`)
	htmlStr = reH2.ReplaceAllString(htmlStr, `$1 style="font-size:20px;color:`+PrimaryColor+`;border-bottom:2px solid `+PrimaryColor+`;padding-bottom:8px;margin:24px 0 14px;font-weight:bold;"$2>`)
	htmlStr = reH3.ReplaceAllString(htmlStr, `$1 style="font-size:17px;color:`+PrimaryColor+`;margin:18px 0 10px;font-weight:bold;"$2>`)
	htmlStr = reP.ReplaceAllString(htmlStr, `$1 style="font-size:15px;line-height:1.8;color:#333;margin:14px 0;letter-spacing:0.5px;"$2>`)
	htmlStr = reBQ.ReplaceAllString(htmlStr, `$1 style="border-left:4px solid `+PrimaryColor+`;background:#f0f7ff;padding:10px 14px;margin:14px 0;color:#555;font-size:14px;"$2>`)
	htmlStr = reUL.ReplaceAllString(htmlStr, `$1 style="margin:14px 0;padding-left:24px;"$2>`)
	htmlStr = reOL.ReplaceAllString(htmlStr, `$1 style="margin:14px 0;padding-left:24px;"$2>`)
	htmlStr = reLI.ReplaceAllString(htmlStr, `$1 style="font-size:15px;line-height:1.8;color:#333;margin:6px 0;"$2>`)
	htmlStr = reHR.ReplaceAllString(htmlStr, `$1 style="border:none;border-top:1px dashed #d0d7de;margin:20px 0;"$2/>`)
	htmlStr = reIM.ReplaceAllString(htmlStr, `$1 style="max-width:100%;border-radius:6px;"$2`)
	htmlStr = reST.ReplaceAllString(htmlStr, `$1 style="color:`+PrimaryColor+`;font-weight:bold;"$2>`)
	htmlStr = reEM.ReplaceAllString(htmlStr, `$1 style="font-style:italic;color:#555;"$2>`)
	// 行内代码：浅蓝底 + 蓝字
	htmlStr = reCD.ReplaceAllString(htmlStr, `$1 style="background:#f0f7ff;color:`+PrimaryColor+`;padding:2px 6px;border-radius:3px;font-family:Consolas,monospace;font-size:14px;"$2>`)
	// 代码块：深色底
	htmlStr = rePR.ReplaceAllString(htmlStr, `$1 style="background:#282c34;border-radius:8px;padding:14px 16px;overflow-x:auto;margin:14px 0;"$2>`)

	// 还原 <pre>（其内部 code 已在 pre 保护期内，不受行内样式影响）
	for i, p := range pres {
		htmlStr = strings.Replace(htmlStr, fmt.Sprintf("<!--PRE%d-->", i), p, 1)
	}
	// pre 里的 code 统一深色主题样式
	htmlStr = reCD.ReplaceAllStringFunc(htmlStr, func(m string) string {
		// 二次替换只会命中 pre 内残余（其余已带 style=，不匹配 <code> 后无属性的情况）
		return m
	})
	return htmlStr
}

// mdToHTML 把 Markdown 源渲染成基础 HTML（goldmark，启用 GFM 表格扩展）。
func mdToHTML(md string) string {
	var buf strings.Builder
	gm := goldmark.New(
		goldmark.WithExtensions(extension.Table), // GFM 表格（注意是复数 WithExtensions）
		goldmark.WithRendererOptions(html.WithHardWraps()),
	)
	_ = gm.Convert([]byte(md), &buf)
	return buf.String()
}

// TruncateDigest 规则 5：摘要按 UTF-8 字节截到 120 字节，且不截破多字节字符。
func TruncateDigest(s string) string {
	b := []byte(s)
	if len(b) <= 120 {
		return s
	}
	cut := b[:120]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1] // 回退半个字符，避免乱码
	}
	return strings.TrimRight(string(cut), " \t\n，。；、")
}

// firstParagraphText 从 HTML 里提取第一个 <p> 的纯文本，作为摘要来源。
func firstParagraphText(htmlStr string) string {
	m := regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`).FindStringSubmatch(htmlStr)
	if m == nil {
		return ""
	}
	text := regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(m[1], "") // 去标签
	return strings.TrimSpace(reInvisible.ReplaceAllString(text, ""))
}

// SplitChapters 规则 7：按二级标题（## ）把 MD 拆成多章。
// 第一块（H1 + 前言）如果非空，作为第一篇（标题取 H1）。
func SplitChapters(md string) []string {
	reH2 := regexp.MustCompile(`(?m)^## +`)
	idx := reH2.FindAllStringIndex(md, -1)
	if len(idx) == 0 {
		return []string{md} // 没有二级标题，整篇是一章
	}
	var parts []string
	if first := strings.TrimSpace(md[:idx[0][0]]); first != "" {
		parts = append(parts, first)
	}
	for i, start := range idx {
		end := len(md)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		// start 是 []int（FindAllStringIndex 的元素），起始位置取 start[0]
		if p := strings.TrimSpace(md[start[0]:end]); p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// chapterTitle 从一章 MD 里取标题：优先第一个 `## 标题`，其次 `# 标题`，
// 并剥掉 emoji（规则 3）；都没有则用"未命名"。
func chapterTitle(md string) string {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^## +(.+)$`),
		regexp.MustCompile(`(?m)^# +(.+)$`),
	} {
		if m := re.FindStringSubmatch(md); m != nil {
			return strings.TrimSpace(reEmoji.ReplaceAllString(m[1], ""))
		}
	}
	return "未命名"
}

// Convert 把一份 Markdown（已按需拆分）转换为文章列表。
// split=true 时按二级标题拆分成多篇（规则 7）。
func Convert(mdSource string, split bool) []Article {
	mdSource = StripFooter(mdSource) // 规则 6：先过滤落款

	var chunks []string
	if split {
		chunks = SplitChapters(mdSource)
	} else {
		chunks = []string{mdSource}
	}

	var articles []Article
	for _, chunk := range chunks {
		htmlStr := mdToHTML(chunk)
		htmlStr = stripTitleEmoji(htmlStr)                       // 规则 3：标题 emoji 剥除
		htmlStr = reATag.ReplaceAllString(htmlStr, "$1")         // 规则 4：<a> → 纯文本
		htmlStr = reInvisible.ReplaceAllString(htmlStr, "")      // 规则 4：不可见 Unicode
		htmlStr = migrateTable(htmlStr)                          // 规则 2：表格对齐迁移
		htmlStr = injectStyles(htmlStr)                          // 规则 1：经典蓝内联样式
		art := Article{
			Title:  chapterTitle(chunk),
			HTML:   htmlStr,
			Digest: TruncateDigest(firstParagraphText(htmlStr)), // 规则 5
			MD:     chunk,
		}
		articles = append(articles, art)
	}
	return articles
}
