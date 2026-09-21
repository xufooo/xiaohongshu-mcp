package xiaohongshu

// xhsVisibleJS 是页面内可见性判定的唯一实现，替代原先散落在 5 处的 visible() 副本。
// 分三层，调用方按需要的严格程度取用：
//
//	visibleInCSS(el)        —— 只判 CSS 可见性（checkVisibility 优先，回退样式）
//	visibleWithSize(el, n)  —— 再要求 rect 宽高都 > n（「看得见且有实体」）
//	visibleOnScreen(el, n)  —— 再要求与视口相交（「点得到」的候选筛选）
//
// 分层而不是一刀切：风控 DOM 证据只要 CSS 可见（宁可多报，失败要响亮），
// 而要点选的元素必须同时满足尺寸与视口相交。
const xhsVisibleJS = `
		const visibleInCSS = (el) => {
			if (!el || !el.isConnected) return false;
			if (typeof el.checkVisibility === "function") {
				return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
			}
			if (el.offsetParent !== null) return true;
			const style = window.getComputedStyle(el);
			return style.display !== "none" &&
				style.visibility !== "hidden" &&
				Number(style.opacity || "1") > 0;
		};
		const visibleWithSize = (el, minSize) => {
			if (!visibleInCSS(el)) return false;
			const size = minSize === undefined ? 0 : minSize;
			const rect = el.getBoundingClientRect();
			return rect.width > size && rect.height > size;
		};
		const visibleOnScreen = (el, minSize) => {
			if (!visibleWithSize(el, minSize)) return false;
			const rect = el.getBoundingClientRect();
			return rect.bottom > 0 && rect.right > 0 &&
				rect.top < window.innerHeight && rect.left < window.innerWidth;
		};
`
