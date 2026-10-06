// Package converter 实现「Markdown → 公众号样式 HTML」的转换。
//
// 【v2.0 · 2026-10-06 多媒体插件校验修复】
// v1.1~v1.9 用 goldmark 默认渲染，Markdown 里的原生 HTML（手写表格、<span>、
// <br> 等）被整体丢弃并替换为 <!-- raw HTML omitted --> 注释，导致：
//   ① 10-06 硬件周报的 11 张 HTML 价格表全部丢失；
//   ② 注释残留使公众号编辑器报「多媒体插件校验出错」。
// v2.0 起对齐 Python 版（convert.py + sanitize_for_wechat）的全部既有规则：
//   1. doocs/md 经典蓝主题（全部样式内联）+ 表格 th/td 默认样式（已有 style 不动）
//   2. 表格 colgroup/col 列宽迁移到首行单元格；th 对齐样式迁移到 td
//   3. 原生 HTML 直通（goldmark html.WithUnsafe，等价 python-markdown 行为）
//   4. 站外链接转文末「参考链接」脚注；<a> 标签上传前一律剥除（API 拒收，45166）
//   5. 不可见 Unicode（零宽/BOM/变体选择符 FE0E/FE0F）与控制字符剥除
//   6. HTML 注释剥除（v1.x 缺失 —— 「多媒体插件校验出错」元凶之一）
//   7. 非 http(s) 图源的 <img> 兜底剔除；style 属性压缩；class 属性剥离
//   8. 标签间空白压缩（防止编辑器把空白文本节点当内容，条款翻倍坑）
//   9. 摘要 120 字节截断（UTF-8 边界安全）
//  10. 落款行过滤（含「本报告由 WorkBuddy…」自动化落款，2026-09-08 方案A）
//  11. frontmatter 剥离；根 <section> 包裹（继承字体基样式）
//  12. 单个 <blockquote> 文字量 ≤400 字自动拆分（v2.1，微信 2026-10 新规：
//      超 ~450 字报「多媒体插件校验出错」，与 Python _split_long_blockquotes 对齐）
package converter

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// PrimaryColor 经典蓝主题主色（doocs/md 的 #1e80ff）。
const PrimaryColor = "#1e80ff"

// rootStyle 根 section 基样式（照搬 Python 版 ROOT_STYLE）。
const rootStyle = "font-family:-apple-system,BlinkMacSystemFont,'Helvetica Neue'," +
	"'Microsoft YaHei',sans-serif;font-size:15px;color:#3e3e3e;line-height:1.75;letter-spacing:0.5px;padding:0 4px;"

// codeBlockSectionStyle 代码块外壳样式（Python 版 CODE_BLOCK_SECTION_STYLE）。
const codeBlockSectionStyle = "background:#f6f8fa;border-radius:4px;padding:12px 14px;margin:0 0 16px 0;" +
	"overflow-x:auto;font-size:13px;line-height:1.6;font-family:Consolas,Menlo,'Courier New',monospace;"

// mpLinkStyle 公众号站内链接样式。
const mpLinkStyle = "color:#576b95;text-decoration:none;"

// Article 表示一篇待发布文章（拆分后的一章 = 一篇草稿）。
type Article struct {
	Title  string // 文章标题（已剥 emoji，直接用作公众号草稿 title）
	HTML   string // 转换后的正文 HTML（内联样式；上传前还需过 SanitizeForWechat）
	Digest string // 摘要（≤120 字节，UTF-8 边界安全）
	MD     string // 对应的原始 Markdown（备用/调试）
}

// ---------- 规则 10：落款行过滤 ----------
var FooterLinePatterns = []*regexp.Regexp{
	// 「本报告由 WorkBuddy 自动化任务生成…」落款（2026-09-08 齐先生拍板方案A，源文件不动）
	regexp.MustCompile(`(?m)^[ \t]*(?:\*{1,3})?[ \t]*本报告由[ \t]*WorkBuddy[^\n]*\n?`),
	// 纯符号装饰行：*** 、--- 、___ 、——— 等（整行只有符号）
	regexp.MustCompile(`(?m)^[ \t]*(?:>+[ \t]*)?[-*_·—=]{3,}[ \t]*$`),
	// 常见落款关键词开头的行（含引用式 "> 本文由..."）
	regexp.MustCompile(`(?m)^[ \t]*(?:>+[ \t]*)?(?:本文由|编辑[:：]|排版[:：]|审校[:：]|欢迎关注|扫码关注|转载请联系|转载请注明|微信号[:：]|公众号[:：]|OldQi Weekly).*$`),
}

// ---------- 规则 3：标题 emoji 剥除 ----------
var reEmoji = regexp.MustCompile(
	`[\x{1F000}-\x{1FAFF}\x{2600}-\x{27BF}\x{2B00}-\x{2BFF}\x{2190}-\x{21FF}\x{FE0F}\x{200D}]`)

// ---------- 规则 5：不可见 Unicode 剥除（含变体选择符 FE0E/FE0F） ----------
var reInvisible = regexp.MustCompile(
	`[\x{200B}-\x{200F}\x{202A}-\x{202E}\x{2060}-\x{2064}\x{FEFF}\x{00AD}\x{180E}\x{FE0E}\x{FE0F}]`)

// ---------- 规则 11：frontmatter 剥离 ----------
var reFrontmatter = regexp.MustCompile(`(?s)\A---[ \t]*\r?\n(.*?)\r?\n---[ \t]*\r?\n?`)

// ---------- 清洗用正则（SanitizeForWechat） ----------
var (
	reATagAny   = regexp.MustCompile(`(?is)<a[^>]*>(.*?)</a>`)          // 所有 <a> → 纯文本
	reComment   = regexp.MustCompile(`(?s)<!--.*?-->`)                  // HTML 注释（「多媒体插件校验出错」元凶）
	reCtrl      = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F]`)    // 控制字符（保留 \t\n\r）
	reClassAttr = regexp.MustCompile(`(?i)\s*class="[^"]*"`)            // class 属性（微信不认）
	reStyleAttr = regexp.MustCompile(`(?i)style="([^"]*)"`)
	reImgTag    = regexp.MustCompile(`(?is)<img[^>]*>`)
	reImgSrc    = regexp.MustCompile(`(?is)<img[^>]*\ssrc="([^"]+)"[^>]*>`)
)

// ---------- 渲染期处理用正则 ----------
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
	reCD = regexp.MustCompile(`(?is)(<code)(\s*)>`)
	rePR = regexp.MustCompile(`(?is)(<pre)(\s*)>`)

	rePre     = regexp.MustCompile(`(?is)<pre\b.*?</pre>`)
	reWS      = regexp.MustCompile(`>\s+<`) // 标签间空白压缩（条款翻倍坑）
	reTitle   = regexp.MustCompile(`(?is)(<h[1-6][^>]*>)(.*?)(</h[1-6]>)`)
	reATagFull = regexp.MustCompile(`(?is)<a([^>]*)>(.*?)</a>`)
	reHref    = regexp.MustCompile(`(?i)href="([^"]*)"`)

	reTable      = regexp.MustCompile(`(?is)<table[^>]*>.*?</table>`)
	reHeadRow    = regexp.MustCompile(`(?is)<thead>(.*?)</thead>`)
	reTh         = regexp.MustCompile(`(?is)<th([^>]*)>(.*?)</th>`)
	reBodyRow    = regexp.MustCompile(`(?is)<tr>(.*?)</tr>`)
	reTdOpen     = regexp.MustCompile(`(?is)(<td)(\s*)>`)
	reAlignStyle = regexp.MustCompile(`text-align:\s*(left|center|right)`)
	reColGroup   = regexp.MustCompile(`(?is)<colgroup>(.*?)</colgroup>`)
	reColWidth   = regexp.MustCompile(`(?is)<col[^>]*width:\s*([\d.]+)%`)
	reCellOpen   = regexp.MustCompile(`(?is)<t([hd])([^>]*)>`)
	reFirstTR    = regexp.MustCompile(`(?is)<tr>.*?</tr>`)
)

// StripFooter 规则 10：按落款模式清单逐条剔除（源文件不动，只清内存副本）。
func StripFooter(md string) string {
	for _, re := range FooterLinePatterns {
		md = re.ReplaceAllString(md, "")
	}
	return md
}

// stripFrontmatter 规则 11：剥离文档头部 YAML frontmatter。
func stripFrontmatter(md string) string {
	return reFrontmatter.ReplaceAllString(md, "")
}

// stripTitleEmoji 规则 3：只在 h1~h6 标签内部剥 emoji（正文里的 emoji 保留）。
func stripTitleEmoji(htmlStr string) string {
	return reTitle.ReplaceAllStringFunc(htmlStr, func(m string) string {
		groups := reTitle.FindStringSubmatch(m)
		return groups[1] + reEmoji.ReplaceAllString(groups[2], "") + groups[3]
	})
}

// ---------- 带 style 守卫的标签样式注入（照搬 Python _add_style_to_tag：
// 已有 style 的标签一律不动，尊重文档自带内联样式） ----------
var tagStyleReCache = map[string]*regexp.Regexp{}

func addStyleGuarded(htmlStr, tag, style string) string {
	re, ok := tagStyleReCache[tag]
	if !ok {
		re = regexp.MustCompile(`(?i)<(` + tag + `)([^>]*?)(/?)>`)
		tagStyleReCache[tag] = re
	}
	return re.ReplaceAllStringFunc(htmlStr, func(m string) string {
		g := re.FindStringSubmatch(m)
		if g == nil {
			return m
		}
		attrs := g[2]
		if strings.Contains(strings.ToLower(attrs), "style=") {
			return m // 已有 style，尊重原样式（Python 版同规则）
		}
		if strings.HasSuffix(attrs, "/") {
			return "<" + g[1] + attrs[:len(attrs)-1] + ` style="` + style + `"/>`
		}
		return "<" + g[1] + attrs + ` style="` + style + `">`
	})
}

// injectStyles 规则 1：经典蓝主题内联样式（普通段落区，不含 pre）。
// h1~h3/p/blockquote 等沿用 v1.x 既有主题（齐先生已认可的版式）；
// table/thead/th/td/del/figure/figcaption 为 v2.0 新补（原版缺失导致表格无框线）。
func injectStyles(htmlStr string) string {
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
	htmlStr = reCD.ReplaceAllString(htmlStr, `$1 style="background:#f0f7ff;color:`+PrimaryColor+`;padding:2px 6px;border-radius:3px;font-family:Consolas,monospace;font-size:14px;"$2>`)

	// v2.0 新补：表格族与删除线（照搬 Python TAG_STYLES；已有 style 的不动）
	htmlStr = addStyleGuarded(htmlStr, "table", "border-collapse:collapse;width:100%;margin:0 0 16px 0;font-size:14px;")
	htmlStr = addStyleGuarded(htmlStr, "thead", "background:#f2f4f7;")
	htmlStr = addStyleGuarded(htmlStr, "th", "border:1px solid #d3d1c7;padding:8px 12px;text-align:left;font-weight:bold;color:#1a1a1a;")
	htmlStr = addStyleGuarded(htmlStr, "td", "border:1px solid #e5e5e5;padding:8px 12px;")
	htmlStr = addStyleGuarded(htmlStr, "del", "text-decoration:line-through;color:#999999;")
	htmlStr = addStyleGuarded(htmlStr, "figure", "margin:0 0 8px 0;text-align:center;")
	htmlStr = addStyleGuarded(htmlStr, "figcaption", "display:block;text-align:center;font-size:13px;color:#888888;margin:4px 0 16px 0;")
	return htmlStr
}

// restylePre 照搬 Python _restyle_pre：代码块套 section 外壳 + 内联样式。
func restylePre(preHTML string) string {
	preHTML = rePR.ReplaceAllString(preHTML, `$1 style="margin:0;white-space:pre-wrap;"$2>`)
	return `<section style="` + codeBlockSectionStyle + `">` + preHTML + `</section>`
}

// footnote 站外链接脚注（Python _convert_links/_footnotes_html 等价实现）。
type footnote struct {
	num  int
	text string
	url  string
}

// convertLinks 规则 4：公众号站外链接转成「文字+[编号]」并汇入文末参考链接。
func convertLinks(htmlStr string, footnotes *[]footnote) string {
	return reATagFull.ReplaceAllStringFunc(htmlStr, func(m string) string {
		g := reATagFull.FindStringSubmatch(m)
		href := ""
		if hm := reHref.FindStringSubmatch(g[1]); hm != nil {
			href = hm[1]
		}
		text := g[2]
		if strings.HasPrefix(href, "https://mp.weixin.qq.com") {
			return `<a href="` + href + `" style="` + mpLinkStyle + `">` + text + `</a>`
		}
		*footnotes = append(*footnotes, footnote{len(*footnotes) + 1, strings.TrimSpace(text), href})
		return text + `<sup style="color:#185fa5;">[` + strconv.Itoa(len(*footnotes)) + `]</sup>`
	})
}

// footnotesHTML 文末「参考链接」小节。
func footnotesHTML(footnotes []footnote) string {
	if len(footnotes) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range footnotes {
		u := ""
		if f.url != "" {
			u = "：" + f.url
		}
		fmt.Fprintf(&b, `<p style="margin:0 0 6px 0;font-size:13px;color:#888888;line-height:1.6;">[%d] %s%s</p>`, f.num, f.text, u)
	}
	return `<section style="margin-top:32px;padding-top:16px;border-top:1px solid #e0e0e0;">` +
		`<p style="margin:0 0 8px 0;font-weight:bold;color:#5f5e5a;">参考链接：</p>` + b.String() + `</section>`
}

// migrateColgroup 规则 2a：colgroup/col 列宽迁移到首行单元格后整块删除
// （照搬 Python sanitize 的 _replace_colgroup；colgroup 是微信白名单外标签）。
func migrateColgroup(htmlStr string) string {
	return reTable.ReplaceAllStringFunc(htmlStr, func(table string) string {
		cg := reColGroup.FindStringSubmatch(table)
		if cg == nil {
			return table
		}
		var widths []string
		for _, w := range reColWidth.FindAllStringSubmatch(cg[1], -1) {
			widths = append(widths, w[1])
		}
		table = strings.Replace(table, cg[0], "", 1)
		if len(widths) == 0 {
			return table
		}
		// 只处理首行（thead 行优先；无 thead 取第一个 tr）
		var scope string
		var scopeStart int
		if head := reHeadRow.FindStringIndex(table); head != nil {
			scope, scopeStart = table[head[0]:head[1]], head[0]
		} else if tr := reFirstTR.FindStringIndex(table); tr != nil {
			scope, scopeStart = table[tr[0]:tr[1]], tr[0]
		} else {
			return table
		}
		col := 0
		newScope := reCellOpen.ReplaceAllStringFunc(scope, func(cell string) string {
			g := reCellOpen.FindStringSubmatch(cell)
			tag, attrs := g[1], g[2]
			if col < len(widths) {
				w := "width:" + widths[col] + "%;"
				col++
				if !strings.Contains(strings.ToLower(attrs), "width:") {
					if sm := reStyleAttr.FindStringSubmatch(attrs); sm != nil {
						return "<t" + tag + strings.Replace(attrs, sm[0], `style="`+w+sm[1]+`"`, 1) + ">"
					}
					return "<t" + tag + ` style="` + w + `"` + attrs + ">"
				}
			}
			return cell
		})
		return table[:scopeStart] + newScope + table[scopeStart+len(scope):]
	})
}

// rowAlignFunc 单行处理：给无 style 的 td 按列序补对齐样式。
func rowAlignFunc(row string, aligns []string) string {
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
		return td[:3] + style + ">"
	})
}

// migrateTableAlign 规则 2b：把 thead 各 th 的对齐样式复制到 tbody 对应 td 上
// （reTdOpen 只匹配无属性的 td，goldmark GFM 表格 td 自带对齐样式不会重复）。
func migrateTableAlign(htmlStr string) string {
	return reTable.ReplaceAllStringFunc(htmlStr, func(table string) string {
		var aligns []string
		if hm := reHeadRow.FindStringSubmatch(table); hm != nil {
			for _, th := range reTh.FindAllStringSubmatch(hm[1], -1) {
				if a := reAlignStyle.FindStringSubmatch(th[1]); a != nil {
					aligns = append(aligns, "text-align:"+a[1])
				} else {
					aligns = append(aligns, "text-align:left")
				}
			}
		}
		if len(aligns) == 0 {
			return table
		}
		return reBodyRow.ReplaceAllStringFunc(table, func(row string) string {
			return rowAlignFunc(row, aligns)
		})
	})
}

// mdToHTML 规则 3：Markdown → 基础 HTML。
// v2.0 关键改动：开启 html.WithUnsafe() —— 原生 HTML 直通
// （v1.x 缺此开关导致手写 HTML 表格被整体丢弃并留下 raw HTML omitted 注释）。
func mdToHTML(md string) string {
	var buf strings.Builder
	gm := goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithRendererOptions(html.WithHardWraps(), html.WithUnsafe()),
	)
	_ = gm.Convert([]byte(md), &buf)
	return buf.String()
}

// TruncateDigest 规则 9：摘要按 UTF-8 字节截到 120 字节，不截破多字节字符。
func TruncateDigest(s string) string {
	b := []byte(s)
	if len(b) <= 120 {
		return s
	}
	cut := b[:120]
	for len(cut) > 0 && !utf8.Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(string(cut), " \t\n，。；、")
}

// firstParagraphText 提取第一个 <p> 的纯文本作摘要来源。
// 与 Python extract_meta 对齐：跳过引用块（> 开头的「采集时间」等元信息行）
// 与代码块，取第一个正文段落。
func firstParagraphText(htmlStr string) string {
	htmlStr = regexp.MustCompile(`(?is)<blockquote[^>]*>.*?</blockquote>`).ReplaceAllString(htmlStr, "")
	htmlStr = regexp.MustCompile(`(?is)<pre\b.*?</pre>`).ReplaceAllString(htmlStr, "")
	m := regexp.MustCompile(`(?is)<p[^>]*>(.*?)</p>`).FindStringSubmatch(htmlStr)
	if m == nil {
		return ""
	}
	text := regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(m[1], "")
	text = strings.TrimSpace(reInvisible.ReplaceAllString(text, ""))
	text = strings.ReplaceAll(text, "\n", " ") // 段内换行压成空格（摘要单行）
	return text
}

// SplitChapters 规则 7：按二级标题（## ）把 MD 拆成多章。
func SplitChapters(md string) []string {
	reH2 := regexp.MustCompile(`(?m)^## +`)
	idx := reH2.FindAllStringIndex(md, -1)
	if len(idx) == 0 {
		return []string{md}
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
		if p := strings.TrimSpace(md[start[0]:end]); p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// chapterTitle 从一章 MD 里取标题并剥 emoji；都没有则用「未命名」。
// 优先 H1（# 主标题，与 Python extract_meta 一致），拆分出的章节块没有 H1 时
// 回退到首个 ## 小节标题。
func chapterTitle(md string) string {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^# +(.+)$`),
		regexp.MustCompile(`(?m)^## +(.+)$`),
	} {
		if m := re.FindStringSubmatch(md); m != nil {
			return strings.TrimSpace(reEmoji.ReplaceAllString(m[1], ""))
		}
	}
	return "未命名"
}

// Convert 把一份 Markdown（已按需拆分）转换为文章列表。
func Convert(mdSource string, split bool) []Article {
	mdSource = stripFrontmatter(mdSource) // 规则 11
	mdSource = StripFooter(mdSource)      // 规则 10

	var chunks []string
	if split {
		chunks = SplitChapters(mdSource)
	} else {
		chunks = []string{mdSource}
	}

	var articles []Article
	for _, chunk := range chunks {
		htmlStr := mdToHTML(chunk)                          // 规则 3：原生 HTML 直通
		htmlStr = reClassAttr.ReplaceAllString(htmlStr, "") // 微信不认 class，先剥
		htmlStr = stripTitleEmoji(htmlStr)                  // 规则 3

		// pre 保护 + 分段处理（照搬 Python _protect_pre 结构：偶数段普通内容，奇数段代码块）
		parts := rePre.Split(htmlStr, -1)
		preMatches := rePre.FindAllString(htmlStr, -1)
		var segs []string
		for i, seg := range parts {
			if i > 0 && i-1 < len(preMatches) {
				segs = append(segs, preMatches[i-1])
			}
			segs = append(segs, seg)
		}
		var footnotes []footnote
		var out []string
		for i, seg := range segs {
			if i%2 == 1 {
				out = append(out, restylePre(seg))
				continue
			}
			seg = injectStyles(seg)
			seg = convertLinks(seg, &footnotes)
			seg = reWS.ReplaceAllString(seg, "><") // 规则 8：标签间空白压缩
			out = append(out, seg)
		}
		htmlStr = strings.Join(out, "")
		htmlStr = migrateColgroup(htmlStr)   // 规则 2a：colgroup 列宽迁移
		htmlStr = migrateTableAlign(htmlStr) // 规则 2b：th 对齐迁移到 td

		// 规则 11：根 section 包裹（含文末脚注区）
		htmlStr = `<section style="` + rootStyle + `">` + htmlStr + footnotesHTML(footnotes) + `</section>`
		htmlStr = SplitLongBlockquotes(htmlStr) // 规则 12（v2.1）：单引用块文字量上限

		articles = append(articles, Article{
			Title:  chapterTitle(chunk),
			HTML:   htmlStr,
			Digest: TruncateDigest(firstParagraphText(htmlStr)), // 规则 9
			MD:     chunk,
		})
	}
	return articles
}

// ------------------------------------------------------------------
// 规则 12：单个引用块文字量上限（v2.1 新增，与 Python convert.py
// _split_long_blockquotes 逐行对齐，2026-10-06 38 篇对照实验实锤）
// ------------------------------------------------------------------

const maxBQText = 400 // 微信 2026-10 新规：blockquote 内文字 >约450 字必炸（430过/500炸），按 400 切留安全边距

var (
	reBQSplit   = regexp.MustCompile(`(?is)(<blockquote[^>]*>)(.*?)</blockquote>`)
	reStripTags = regexp.MustCompile(`<[^>]+>`)
	reBQPLeft   = regexp.MustCompile(`^\s*<p[^>]*>`)
	reBQPRight  = regexp.MustCompile(`</p>\s*$`)
)

// bqTextLen 引用块内纯文字量（按字符数计，与 Python len 一致，含标签间换行）。
func bqTextLen(s string) int {
	return utf8.RuneCountInString(reStripTags.ReplaceAllString(s, ""))
}

// bqTagSafeNewlines 返回不在标签内部的换行符位置（rune 下标）。
func bqTagSafeNewlines(inner []rune) []int {
	var res []int
	depth := 0
	for i, ch := range inner {
		switch {
		case ch == '<':
			depth++
		case ch == '>':
			if depth > 0 {
				depth--
			}
		case ch == '\n' && depth == 0:
			res = append(res, i)
		}
	}
	return res
}

// bqHardSplit 纯文本兜底切分：句号优先，单句超长再按长度硬切。
func bqHardSplit(text string, maxText int) []string {
	runes := []rune(text)
	var segs []string
	start := 0
	for i, r := range runes { // 按句号切（句号留在前段，等效 Python (?<=。)）
		if r == '。' {
			segs = append(segs, string(runes[start:i+1]))
			start = i + 1
		}
	}
	if start < len(runes) {
		segs = append(segs, string(runes[start:]))
	}
	var pieces []string
	buf := ""
	for _, seg := range segs {
		if buf != "" && utf8.RuneCountInString(buf)+utf8.RuneCountInString(seg) > maxText {
			pieces = append(pieces, buf)
			buf = seg
		} else {
			buf += seg
		}
	}
	if buf != "" {
		pieces = append(pieces, buf)
	}
	var out []string
	for _, p := range pieces { // 单句仍超长 → 硬切
		r := []rune(p)
		for utf8.RuneCountInString(string(r)) > maxText {
			out = append(out, string(r[:maxText]))
			r = r[maxText:]
		}
		if len(r) > 0 {
			out = append(out, string(r))
		}
	}
	return out
}

// SplitLongBlockquotes 把超长 <blockquote> 按以下策略拆成多个连续引用块：
//  1. 优先按「标签外的换行符」切（清单条目边界，保留每条的内联样式）；
//  2. 切出的片若仍超长，按句号切；再不行按长度硬切。
//  3. 每片补齐 <p>...</p>，编辑器对标签不平衡宽容（实测截断稿可通过）。
func SplitLongBlockquotes(htmlStr string) string {
	return reBQSplit.ReplaceAllStringFunc(htmlStr, func(m string) string {
		g := reBQSplit.FindStringSubmatch(m)
		openTag, inner := g[1], g[2]
		if bqTextLen(inner) <= maxBQText {
			return m // 不超长，原样
		}
		innerR := []rune(inner)
		nls := bqTagSafeNewlines(innerR)
		var chunks []string
		if len(nls) > 0 {
			bounds := []int{0}
			for _, n := range nls {
				bounds = append(bounds, n+1)
			}
			bounds = append(bounds, len(innerR))
			var buf []rune
			for i := 0; i < len(bounds)-1; i++ {
				p := innerR[bounds[i]:bounds[i+1]]
				if len(buf) > 0 && bqTextLen(string(buf))+bqTextLen(string(p)) > maxBQText {
					chunks = append(chunks, string(buf))
					buf = append([]rune(nil), p...)
				} else {
					buf = append(buf, p...)
				}
			}
			if strings.TrimSpace(string(buf)) != "" {
				chunks = append(chunks, string(buf))
			}
		} else {
			chunks = []string{inner}
		}
		var out []string
		for _, ch := range chunks {
			if strings.TrimSpace(ch) == "" {
				continue
			}
			if bqTextLen(ch) <= maxBQText {
				// 补齐段落标签，保持每片结构完整
				c := strings.TrimLeft(ch, " \t\r\n")
				if !strings.HasPrefix(c, "<p") {
					c = "<p>" + c
				}
				c = strings.TrimRight(c, " \t\r\n")
				if !strings.HasSuffix(c, "</p>") {
					c += "</p>"
				}
				out = append(out, openTag+c+"</blockquote>")
			} else {
				plain := reBQPRight.ReplaceAllString(
					reBQPLeft.ReplaceAllString(strings.TrimSpace(ch), ""), "")
				for _, piece := range bqHardSplit(plain, maxBQText) {
					out = append(out, openTag+"<p>"+piece+"</p></blockquote>")
				}
			}
		}
		return strings.Join(out, "")
	})
}

// SanitizeForWechat 上传草稿前的内容清洗（照搬 Python sanitize_for_wechat，
// 必须在图片链接替换之后调用）：
//   1. 剥离所有 <a> 标签只留文字 —— API 传入 <a> 触发 45166
//   2. 清除不可见 Unicode（零宽/BOM/变体选择符 FE0E/FE0F）
//   3. 清除 HTML 注释（v1.x 缺失 —— 编辑器「多媒体插件校验出错」元凶）
//      与 ASCII 控制字符
//   4. 移除未能替换为微信图链的 <img>（本地路径图片微信不收）
//   5. 压缩 style 属性体积
func SanitizeForWechat(htmlStr string) string {
	htmlStr = reATagAny.ReplaceAllString(htmlStr, "$1")
	htmlStr = reInvisible.ReplaceAllString(htmlStr, "")
	htmlStr = reComment.ReplaceAllString(htmlStr, "")
	htmlStr = reCtrl.ReplaceAllString(htmlStr, "")
	htmlStr = reImgTag.ReplaceAllStringFunc(htmlStr, func(tag string) string {
		m := reImgSrc.FindStringSubmatch(tag)
		if m == nil {
			return "" // 无 src 的 img 直接剔
		}
		if strings.HasPrefix(m[1], "http://") || strings.HasPrefix(m[1], "https://") {
			return tag // 微信图链/网络图，保留
		}
		return "" // 本地路径残留，剔
	})
	htmlStr = reStyleAttr.ReplaceAllStringFunc(htmlStr, func(m string) string {
		g := reStyleAttr.FindStringSubmatch(m)
		s := regexp.MustCompile(`\s*;\s*`).ReplaceAllString(g[1], ";")
		s = regexp.MustCompile(`\s*:\s*`).ReplaceAllString(s, ":")
		s = regexp.MustCompile(`;{2,}`).ReplaceAllString(s, ";")
		return `style="` + s + `"`
	})
	return htmlStr
}
