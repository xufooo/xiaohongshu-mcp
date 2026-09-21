package xiaohongshu

// xhsScrollYJS 是页面内「阅读位置」的唯一实现，替代原先三处内联的
// `window.scrollY || document.scrollingElement.scrollTop`。
//
// 为什么不能只看窗口：实测详情页与搜索结果页都**不靠窗口滚动**，内容滚在
// 内层容器里（D.15 一手数据：详情页 `.note-scroller` scrollTop 0 → 2335；
// 搜索页 `.search-layout-wrapper` scrollTop 0 → 1420；而 window.scrollY 恒为 0）。
// 所以窗口值为 0 时才回退到这两个实测命中的容器，取「可滚动量更大」的那个。
//
// 只列这两个容器：它们是实测观测到的滚动宿主，不做全树扫描——
// 这个函数在每次就绪探测里都会执行，全树 getComputedStyle 在 Pi 上代价过高。
const xhsScrollYJS = `
		const scrollY = () => {
			const windowY = Math.round(window.scrollY || document.scrollingElement?.scrollTop || 0);
			if (windowY > 0) return windowY;
			let best = 0;
			let bestRange = 0;
			for (const selector of [".note-scroller", ".search-layout-wrapper"]) {
				const el = document.querySelector(selector);
				if (!el) continue;
				const range = el.scrollHeight - el.clientHeight;
				if (range <= 4) continue;
				if (range > bestRange) {
					bestRange = range;
					best = Math.round(el.scrollTop);
				}
			}
			return best;
		};
`
