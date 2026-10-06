// 转换测试：v2.0 转换器回归验证工具（不依赖 Fyne，`go run ./转换测试` 即可跑）。
//
// 用法：go run ./转换测试 <MD文件路径...>
// 对每篇 MD 执行 Convert + SanitizeForWechat，输出统计并写出 HTML 供比对：
//   表格数 / 注释数（应为0）/ raw HTML omitted 残留（应为0）/ 图片数 / 字符数
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"wechat-mp-publisher/converter"
)

var (
	reBQ   = regexp.MustCompile(`(?is)<blockquote[^>]*>.*?</blockquote>`)
	reTag  = regexp.MustCompile(`<[^>]+>`)
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法：go run ./转换测试 <MD文件路径...>")
		os.Exit(1)
	}
	for _, p := range os.Args[1:] {
		raw, err := os.ReadFile(p)
		if err != nil {
			fmt.Println("读取失败:", err)
			continue
		}
		arts := converter.Convert(string(raw), false)
		fmt.Println("=" + strings.Repeat("=", 50))
		for _, a := range arts {
			html := converter.SanitizeForWechat(a.HTML)
			html = converter.SplitLongBlockquotes(html) // 兜底（Convert 内已跑，此处验幂等）
			name := filepath.Base(p)
			out := filepath.Join(os.TempDir(), "v2测试-"+name+".html")
			_ = os.WriteFile(out, []byte(html), 0644)
			// 规则 12 回归指标：统计每个 blockquote 的纯文字量
			maxBQ, bqCount := 0, 0
			for _, m := range reBQ.FindAllString(html, -1) {
				bqCount++
				plain := reTag.FindAllString(m, -1)
				n := len([]rune(m))
				for _, t := range plain {
					n -= len(t)
				}
				if n > maxBQ {
					maxBQ = n
				}
			}
			fmt.Printf("《%s》\n", a.Title)
			fmt.Printf("  表格数=%d  注释数=%d  rawHTML残留=%d  图片数=%d  字符数=%d\n",
				strings.Count(html, "<table"),
				strings.Count(html, "<!--"),
				strings.Count(html, "raw HTML omitted"),
				strings.Count(html, "<img"),
				len([]rune(html)))
			fmt.Printf("  引用块数=%d  最大引用块文字量=%d（应≤400）\n", bqCount, maxBQ)
			fmt.Printf("  摘要: %s\n", a.Digest)
			fmt.Printf("  输出: %s\n", out)
		}
	}
}
