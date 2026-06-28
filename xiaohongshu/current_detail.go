package xiaohongshu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/go-rod/rod/lib/cdp"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const xhsProbeVisibleJS = `
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
`

const xhsProbeFeedMatchJS = `
			const detailURLMatchesFeedID = (rawURL) => {
				if (!feedID) return false;
				try {
					const parsed = new URL(String(rawURL || ""), location.href);
					const segments = parsed.pathname.split("/").filter(Boolean).map((part) => decodeURIComponent(part));
					if (segments.includes(feedID)) return true;
					for (const value of parsed.searchParams.values()) {
						if (value === feedID) return true;
					}
				} catch (_) {
					return false;
				}
				return false;
			};
			const elementMatchesFeedID = (el) => {
				if (!feedID || !el) return false;
				if (Object.values(el.dataset || {}).some((value) => String(value || "") === feedID)) {
					return true;
				}
				return Array.from(el.querySelectorAll("a[href]")).some((a) => detailURLMatchesFeedID(a.href));
			};
`

var errPermanentCurrentDetailProbe = errors.New("permanent current detail probe error")

type currentFeedDetailProbe struct {
	URL                       string `json:"url"`
	URLMatched                bool   `json:"url_matched"`
	VisibleDetailCount        int    `json:"visible_detail_count"`
	VisibleMatchedDetailCount int    `json:"visible_matched_detail_count"`
	StateMatched              bool   `json:"state_matched"`
}

func probeCurrentFeedDetail(page *hrod.Page, feedID string) (currentFeedDetailProbe, error) {
	probeJS := `(feedID, detailSelector) => {` + xhsProbeVisibleJS + xhsProbeFeedMatchJS + `
			const visibleDetails = Array.from(document.querySelectorAll(detailSelector)).filter(visible);
			const visibleMatchedDetails = visibleDetails.filter(elementMatchesFeedID);
			const stateMap = window.__INITIAL_STATE__?.note?.noteDetailMap;
			return JSON.stringify({
				url: location.href.slice(0, 300),
				url_matched: detailURLMatchesFeedID(location.href),
				visible_detail_count: visibleDetails.length,
				visible_matched_detail_count: visibleMatchedDetails.length,
				state_matched: Boolean(feedID && stateMap && Object.prototype.hasOwnProperty.call(stateMap, feedID)),
		});
	}`
	obj, err := page.Eval(probeJS, feedID, SelectorFeedDetailReady)
	if err != nil {
		return currentFeedDetailProbe{}, err
	}
	if obj == nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: 当前详情页探测无返回", errPermanentCurrentDetailProbe)
	}

	var probe currentFeedDetailProbe
	if err := json.Unmarshal([]byte(obj.Value.Str()), &probe); err != nil {
		return currentFeedDetailProbe{}, fmt.Errorf("%w: %v", errPermanentCurrentDetailProbe, err)
	}
	return probe, nil
}

func currentFeedDetailMatched(probe currentFeedDetailProbe, _ string) bool {
	return (probe.URLMatched && probe.VisibleDetailCount > 0) ||
		probe.VisibleMatchedDetailCount > 0
}

func detailURLMatchesFeedID(rawURL, feedID string) bool {
	if feedID == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	for _, segment := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if segment == feedID {
			return true
		}
	}
	for _, values := range u.Query() {
		for _, value := range values {
			if value == feedID {
				return true
			}
		}
	}
	return false
}

func isTransientCurrentDetailProbeError(err error) bool {
	if err == nil || errors.Is(err, errPermanentCurrentDetailProbe) {
		return false
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, cdp.ErrCtxNotFound) ||
		errors.Is(err, cdp.ErrCtxDestroyed) {
		return true
	}

	message := err.Error()
	return strings.Contains(message, "Execution context was destroyed") ||
		strings.Contains(message, "Cannot find context with specified id") ||
		strings.Contains(message, "context canceled")
}
