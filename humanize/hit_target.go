package humanize

// HitTargetJS 声明页面内的命中判据 `hitTargets(el, hit)`，供各处注入的 JS **字符串拼接**复用
// （与 xiaohongshu 里 xhsVisibleJS 的做法一致：页面内函数片段，而不是跨 CDP 传函数值）。
//
// 为什么需要它：`document.elementFromPoint` 对 **shadow DOM 内部**的节点会做命中重定向——
// 返回的是 shadow 宿主（host），而不是内部那个元素。closed shadow root 尤其明显：
// 小红书发布页的 `xhs-publish-btn` 内部按钮，用 `hit === this || this.contains(hit)`
// 永远判不中，于是永远"不可点击 / 落点不命中目标"，发布最后一击失败。
//
// 判据顺序：元素自身 → 元素子孙 → 元素所在 shadow root 的宿主。
const HitTargetJS = `const hitTargets = (el, hit) => {
	if (!el || !hit) return false;
	if (el === hit || el.contains(hit)) return true;
	const root = el.getRootNode ? el.getRootNode() : null;
	const host = root && root.host ? root.host : null;
	return !!(host && (hit === host || host.contains(hit)));
};`
