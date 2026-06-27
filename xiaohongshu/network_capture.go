package xiaohongshu

import (
	"context"
	"encoding/base64"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/proto"
	hrod "github.com/xpzouying/xiaohongshu-mcp/pkg/humanize/rod"
)

const (
	defaultNetworkCaptureLimit      = 20
	maxNetworkCaptureBodyBytes      = 128 * 1024
	maxNetworkCaptureSummaryRunes   = 512
	networkCaptureStopWait         = 800 * time.Millisecond
	networkCaptureBodyFetchTimeout = 1500 * time.Millisecond
)

type NetworkCaptureOptions struct {
	Limit int
}

type NetworkCaptureEntry struct {
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type,omitempty"`
	BodySummary string `json:"body_summary,omitempty"`

	requestID string
}

type NetworkCapture struct {
	page   *hrod.Page
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	limit   int
	entries []NetworkCaptureEntry

	bodyWG sync.WaitGroup
}

func StartNetworkCapture(page *hrod.Page, opts NetworkCaptureOptions) *NetworkCapture {
	if page == nil || page.Rod == nil {
		return nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = defaultNetworkCaptureLimit
	}

	ctx, cancel := context.WithCancel(context.Background())
	capture := &NetworkCapture{
		page:   page,
		cancel: cancel,
		done:   make(chan struct{}),
		limit:  limit,
	}
	eventPage := page.Rod.Context(ctx)

	go func() {
		defer close(capture.done)
		eventPage.EachEvent(
			func(e *proto.NetworkResponseReceived) {
				capture.onResponseReceived(e)
			},
			func(e *proto.NetworkLoadingFinished) {
				capture.onLoadingFinished(e)
			},
		)()
	}()

	return capture
}

func (c *NetworkCapture) Stop() []NetworkCaptureEntry {
	if c == nil {
		return nil
	}
	c.cancel()
	select {
	case <-c.done:
	case <-time.After(networkCaptureStopWait):
		return c.Snapshot()
	}

	bodyDone := make(chan struct{})
	go func() {
		c.bodyWG.Wait()
		close(bodyDone)
	}()
	select {
	case <-bodyDone:
	case <-time.After(networkCaptureStopWait):
	}

	return c.Snapshot()
}

func (c *NetworkCapture) Snapshot() []NetworkCaptureEntry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entries := make([]NetworkCaptureEntry, len(c.entries))
	for i, entry := range c.entries {
		entry.requestID = ""
		entries[i] = entry
	}
	return entries
}

func (c *NetworkCapture) onResponseReceived(e *proto.NetworkResponseReceived) {
	if e == nil || e.Response == nil || !shouldCaptureNetworkResponse(e.Response.URL, e.Response.MIMEType) {
		return
	}
	entry := NetworkCaptureEntry{
		URL:         trimNetworkCaptureURL(redactSensitiveURL(e.Response.URL)),
		Status:      e.Response.Status,
		ContentType: e.Response.MIMEType,
		requestID:   string(e.RequestID),
	}
	c.upsert(entry)
}

func (c *NetworkCapture) onLoadingFinished(e *proto.NetworkLoadingFinished) {
	if e == nil || e.EncodedDataLength <= 0 || e.EncodedDataLength > maxNetworkCaptureBodyBytes {
		return
	}
	entry, ok := c.find(string(e.RequestID))
	if !ok || !isSummarizableContentType(entry.ContentType) {
		return
	}

	c.bodyWG.Add(1)
	go func(requestID proto.NetworkRequestID) {
		defer c.bodyWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), networkCaptureBodyFetchTimeout)
		defer cancel()

		result, err := (proto.NetworkGetResponseBody{RequestID: requestID}).Call(c.page.Rod.Context(ctx))
		if err != nil || result == nil {
			return
		}
		body := result.Body
		if result.Base64Encoded {
			decoded, err := base64.StdEncoding.DecodeString(body)
			if err != nil {
				return
			}
			body = string(decoded)
		}
		entry.BodySummary = summarizeNetworkBody(body)
		c.upsert(entry)
	}(e.RequestID)
}

func (c *NetworkCapture) find(requestID string) (NetworkCaptureEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		if entry.requestID == requestID {
			return entry, true
		}
	}
	return NetworkCaptureEntry{}, false
}

func (c *NetworkCapture) upsert(entry NetworkCaptureEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.entries {
		if c.entries[i].requestID == entry.requestID {
			c.entries[i] = entry
			return
		}
	}

	c.entries = append(c.entries, entry)
	if len(c.entries) > c.limit {
		c.entries = append([]NetworkCaptureEntry(nil), c.entries[len(c.entries)-c.limit:]...)
	}
}

func shouldCaptureNetworkResponse(rawURL, contentType string) bool {
	if rawURL == "" {
		return false
	}
	lowerURL := strings.ToLower(rawURL)
	if !strings.Contains(lowerURL, "xiaohongshu.com") && !strings.Contains(lowerURL, "xhscdn.com") {
		return false
	}
	if strings.Contains(lowerURL, "/api/") {
		return true
	}
	return strings.Contains(strings.ToLower(contentType), "json")
}

func isSummarizableContentType(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "text") ||
		strings.Contains(contentType, "javascript")
}

func summarizeNetworkBody(body string) string {
	summary := redactSensitiveText(strings.Join(strings.Fields(body), " "))
	runes := []rune(summary)
	if len(runes) > maxNetworkCaptureSummaryRunes {
		summary = string(runes[:maxNetworkCaptureSummaryRunes]) + "..."
	}
	return summary
}

func trimNetworkCaptureURL(rawURL string) string {
	const maxURLRunes = 300
	runes := []rune(rawURL)
	if len(runes) <= maxURLRunes {
		return rawURL
	}
	return string(runes[:maxURLRunes]) + "..."
}
