package xiaohongshu

import (
	"encoding/json"
	"fmt"

	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

type currentFeedDetailProbe struct {
	URL                       string `json:"url"`
	URLMatched                bool   `json:"url_matched"`
	VisibleDetailCount        int    `json:"visible_detail_count"`
	VisibleMatchedDetailCount int    `json:"visible_matched_detail_count"`
	StateMatched              bool   `json:"state_matched"`
}

func probeCurrentFeedDetail(page *hrod.Page, feedID string) (currentFeedDetailProbe, error) {
	obj, err := page.Eval(`(feedID, detailSelector) => {
		const visible = (el) => {
			if (!el || !el.isConnected) return false;
			if (typeof el.checkVisibility === "function") {
				return el.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true });
			}
			const rect = el.getBoundingClientRect();
			const style = window.getComputedStyle(el);
			return style.display !== "none" &&
				style.visibility !== "hidden" &&
				Number(style.opacity || "1") > 0 &&
				rect.width > 1 &&
				rect.height > 1;
		};
		const includesFeed = (value) => Boolean(feedID && String(value || "").includes(feedID));
		const visibleDetails = Array.from(document.querySelectorAll(detailSelector)).filter(visible);
		const visibleMatchedDetails = visibleDetails.filter((el) => {
			const data = JSON.stringify(el.dataset || {});
			const links = Array.from(el.querySelectorAll("a[href]")).map((a) => a.href).join(" ");
			return includesFeed(data) || includesFeed(links);
		});
		const stateMap = window.__INITIAL_STATE__?.note?.noteDetailMap;
		return JSON.stringify({
			url: location.href.slice(0, 300),
			url_matched: includesFeed(location.href),
			visible_detail_count: visibleDetails.length,
			visible_matched_detail_count: visibleMatchedDetails.length,
			state_matched: Boolean(feedID && stateMap && Object.prototype.hasOwnProperty.call(stateMap, feedID)),
		});
	}`, feedID, SelectorFeedDetailReady)
	if err != nil {
		return currentFeedDetailProbe{}, err
	}
	if obj == nil {
		return currentFeedDetailProbe{}, fmt.Errorf("当前详情页探测无返回")
	}

	var probe currentFeedDetailProbe
	if err := json.Unmarshal([]byte(obj.Value.Str()), &probe); err != nil {
		return currentFeedDetailProbe{}, err
	}
	return probe, nil
}

func currentFeedDetailMatched(probe currentFeedDetailProbe) bool {
	return (probe.URLMatched && probe.VisibleDetailCount > 0) || probe.VisibleMatchedDetailCount > 0
}
