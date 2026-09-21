package humanize

import (
	"errors"
	"fmt"

	"github.com/go-rod/rod"
)

// coverDismissJS 在"目标被浮层盖住"时，把浮层自己的关闭控件标记出来，
// 供 Go 侧用元素级点击关掉它（不猜坐标）。
//
// 背景（一手实测）：小红书创作页在图片上传后会弹「图片可以编辑啦」新功能引导
// （role=dialog 的 .feature-guide 浮层，z-index 999），正好盖住标题输入框。
// 它不会自己消失，Escape 也关不掉；唯一有效的是点它自带的关闭控件：
// <button aria-label="关闭新功能引导"> 或 <button class="feature-guide__btn">我知道了</button>。
// 注意按钮可能落在视口之外（实测 780x459 视口里 "我知道了" 在 y=509），所以只挑**视口内可见**的那个。
const coverDismissJS = `() => {
	document.querySelectorAll("[data-xhs-mcp-cover-close]").forEach((n) => n.removeAttribute("data-xhs-mcp-cover-close"));
	const r = this.getBoundingClientRect();
	const hit = document.elementFromPoint((r.left + r.right) / 2, (r.top + r.bottom) / 2);
	if (!hit) return "";
	let node = hit;
	while (node && node !== document.body) {
		const cls = String(node.className || "");
		const isDialog = (node.getAttribute && node.getAttribute("role") === "dialog") || cls.indexOf("d-popover") >= 0 || cls.indexOf("d-modal") >= 0;
		if (isDialog) break;
		node = node.parentElement;
	}
	if (!node || node === document.body) return "";
	for (const sel of ['[aria-label*="关闭"]', '[class*="close"]', '[class*="know"]', "button"]) {
		for (const c of node.querySelectorAll(sel)) {
			const b = c.getBoundingClientRect();
			const s = getComputedStyle(c);
			const inViewport = b.width > 1 && b.height > 1 && b.top >= 0 && b.left >= 0 && b.bottom <= innerHeight && b.right <= innerWidth;
			if (!inViewport || s.visibility === "hidden" || Number(s.opacity || "1") === 0) continue;
			c.setAttribute("data-xhs-mcp-cover-close", "1");
			return String(c.className || c.tagName || "close");
		}
	}
	return "";
}`

// dismissCoverDialog 关掉盖住目标元素的浮层：点它自己的关闭控件（元素级点击，不猜坐标）。
// 找不到关闭控件就直接报错——不等待、不按 Escape、不换策略。
func (m *Mouse) dismissCoverDialog(target *rod.Element) error {
	page := m.boundPage()
	obj, err := target.Context(m.ctx).Eval(coverDismissJS)
	if err != nil {
		return err
	}
	if obj == nil || obj.Value.Str() == "" {
		return errors.New("浮层没有可用的关闭控件")
	}
	button, err := page.Element("[data-xhs-mcp-cover-close]")
	if err != nil {
		return fmt.Errorf("定位浮层关闭控件失败: %w", err)
	}
	return m.ClickNoScroll(button)
}
