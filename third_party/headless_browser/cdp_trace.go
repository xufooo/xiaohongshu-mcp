package headless_browser

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/utils"
)

type redactedCDPLogger struct {
	mu       sync.Mutex
	w        io.Writer
	next     int
	sessions map[string]string
	requests map[int]string
}

func newRedactedCDPLogger(w io.Writer) utils.Logger {
	return &redactedCDPLogger{w: w, sessions: make(map[string]string), requests: make(map[int]string)}
}

func (l *redactedCDPLogger) Println(values ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, value := range values {
		switch message := value.(type) {
		case *cdp.Request:
			session := l.sessionAlias(message.SessionID)
			l.requests[message.ID] = session
			fmt.Fprintf(l.w, "%s request id=%d session=%s method=%s\n", now, message.ID, session, message.Method)
		case *cdp.Response:
			fmt.Fprintf(l.w, "%s response id=%d session=%s error=%t\n", now, message.ID, l.requests[message.ID], message.Error != nil)
			delete(l.requests, message.ID)
		case *cdp.Event:
			if message.Method != "Target.attachedToTarget" && message.Method != "Target.detachedFromTarget" && message.Method != "Target.targetDestroyed" {
				continue
			}
			var params struct {
				SessionID string `json:"sessionId"`
				TargetID  string `json:"targetId"`
			}
			_ = json.Unmarshal(message.Params, &params)
			if params.SessionID != "" {
				fmt.Fprintf(l.w, "%s event method=%s session=%s\n", now, message.Method, l.sessionAlias(params.SessionID))
			} else {
				fmt.Fprintf(l.w, "%s event method=%s target=%t\n", now, message.Method, params.TargetID != "")
			}
		}
	}
}

func (l *redactedCDPLogger) sessionAlias(raw string) string {
	if raw == "" {
		return "browser"
	}
	if alias := l.sessions[raw]; alias != "" {
		return alias
	}
	l.next++
	alias := fmt.Sprintf("s%d", l.next)
	l.sessions[raw] = alias
	return alias
}
