package headless_browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-rod/rod/lib/cdp"
	"github.com/go-rod/rod/lib/proto"
)

const maxTraceEntries = 1024

type traceRequest struct { started time.Time; session, method string }
type redactedCDPLogger struct {
	mu sync.Mutex; w io.Writer; run string; next int
	sessions map[string]string; requests map[int]traceRequest
}
func newRedactedCDPLogger(w io.Writer) (*redactedCDPLogger, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	return &redactedCDPLogger{w:w, run:"r_"+hex.EncodeToString(b[:]), sessions:map[string]string{}, requests:map[int]traceRequest{}}, nil
}
func (l *redactedCDPLogger) Println(values ...interface{}) {
	l.mu.Lock(); defer l.mu.Unlock(); now:=time.Now().UTC()
	for _, value:=range values { switch m:=value.(type) {
	case *cdp.Request:
		s:=l.sessionAlias(m.SessionID); l.trimRequests(); l.requests[m.ID]=traceRequest{now,s,m.Method}
		if m.Method=="Runtime.evaluate" { runtimeRequest(l, now, m.ID, s, m.Params) } else { l.line(now,"request id=%d session=%s method=%s",m.ID,s,m.Method) }
	case *cdp.Response:
		r,ok:=l.requests[m.ID]; latency:=int64(-1); session,method:="unknown","unknown"
		if ok { latency=now.Sub(r.started).Milliseconds(); session,method=r.session,r.method }
		code:=0; cat:="none"
		if m.Error!=nil { code=m.Error.Code; cat=traceError(m.Error.Message) }
		extra:=""; if method=="Runtime.evaluate" { hasExc,parseOK:=evaluateFlags(m.Result); extra=fmt.Sprintf(" has_exception_details=%t result_parse_ok=%t",hasExc,parseOK) }
		l.line(now,"response id=%d session=%s method=%s latency_ms=%d error=%t error_code=%d error_category=%s%s",m.ID,session,method,latency,m.Error!=nil,code,cat,extra); delete(l.requests,m.ID)
	case *cdp.Event:
		l.event(now,m)
	} }
}
func runtimeRequest(l *redactedCDPLogger, t time.Time, id int, s string, raw interface{}) {
	var p proto.RuntimeEvaluate
	switch v := raw.(type) { case proto.RuntimeEvaluate: p=v; case *proto.RuntimeEvaluate: if v!=nil {p=*v} }
	l.line(t,"request id=%d session=%s method=Runtime.evaluate timeout_ms=%.3f await_promise=%t return_by_value=%t context_id_set=%t unique_context_id_set=%t expression_size=%s",id,s,p.Timeout,p.AwaitPromise,p.ReturnByValue,p.ContextID!=0,p.UniqueContextID!="",sizeBucket(len(p.Expression)))
}
func (l *redactedCDPLogger) event(t time.Time,m *cdp.Event) {
	allowed:=map[string]bool{"Target.attachedToTarget":true,"Target.detachedFromTarget":true,"Target.targetDestroyed":true,"Page.lifecycleEvent":true,"Page.frameNavigated":true,"Runtime.executionContextCreated":true,"Runtime.executionContextDestroyed":true,"Runtime.executionContextsCleared":true}
	if !allowed[m.Method] { return }; s:=l.sessionAlias(m.SessionID); l.line(t,"event method=%s session=%s",m.Method,s)
}
func (l *redactedCDPLogger) line(t time.Time,f string,a ...interface{}) { fmt.Fprintf(l.w,"%s run=%s "+f+"\n",append([]interface{}{t.Format(time.RFC3339Nano),l.run},a...)...) }
func (l *redactedCDPLogger) trimRequests() { if len(l.requests)<maxTraceEntries{return}; var old int; var at time.Time; for id,r:=range l.requests {if at.IsZero()||r.started.Before(at){old,at=id,r.started}}; delete(l.requests,old) }
func (l *redactedCDPLogger) setup(runtimeErr, lifecycleErr error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s run=%s diag_setup runtime=%s lifecycle=%s\n", time.Now().UTC().Format(time.RFC3339Nano), l.run, diagStatus(runtimeErr), diagStatus(lifecycleErr))
}
func diagStatus(err error) string { if err == nil { return "ok" }; return "error" }

func (l *redactedCDPLogger) sessionAlias(raw string) string { if raw=="" { return "browser" }; if x:=l.sessions[raw];x!="" {return x}; if len(l.sessions)>=maxTraceEntries{return "overflow"}; l.next++; x:=fmt.Sprintf("s%d",l.next); l.sessions[raw]=x; return x }
func sizeBucket(n int) string { switch {case n==0:return "empty";case n<1000:return "lt1k";case n<4000:return "1k_4k";case n<16000:return "4k_16k";default:return "ge16k"} }
func traceError(s string) string { s=strings.ToLower(s); switch {case strings.Contains(s,"context"):return "context_destroyed";case strings.Contains(s,"terminated"):return "execution_terminated";case strings.Contains(s,"timeout"):return "timeout";case strings.Contains(s,"session"):return "session_closed";default:return "other"} }
func evaluateFlags(raw json.RawMessage)(bool,bool){ var v struct{ Exception json.RawMessage `json:"exceptionDetails"` }; if len(raw)==0{return false,false}; if json.Unmarshal(raw,&v)!=nil{return false,false}; return len(v.Exception)>0 && string(v.Exception)!="null",true }

type diagnosticCDPClient struct { raw *cdp.Client; browserCtx context.Context; logger *redactedCDPLogger; mu sync.Mutex; armed map[string]bool; issued map[string]bool }
func newDiagnosticCDPClient(raw *cdp.Client, browserCtx context.Context,logger *redactedCDPLogger)*diagnosticCDPClient{return &diagnosticCDPClient{raw:raw,browserCtx:browserCtx,logger:logger,armed:map[string]bool{},issued:map[string]bool{}}}
func (c *diagnosticCDPClient) Event()<-chan *cdp.Event{return c.raw.Event()}
func (c *diagnosticCDPClient) Arm(session proto.TargetSessionID){c.mu.Lock();if len(c.armed)<maxTraceEntries {c.armed[string(session)]=true};c.mu.Unlock()}
func (c *diagnosticCDPClient) Call(ctx context.Context,session,method string,params interface{})([]byte,error){
	if method!="Runtime.evaluate" { return c.raw.Call(ctx,session,method,params) }
	c.mu.Lock(); armed:=c.armed[session]&&!c.issued[session]; c.mu.Unlock()
	if armed {
		var finished atomic.Bool
		done := make(chan struct{})
		go func() {
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-timer.C:
				c.mu.Lock()
				if !finished.Load() && c.armed[session] && !c.issued[session] {
					c.issued[session] = true
					go c.probes(session)
				}
				c.mu.Unlock()
			case <-done:
			case <-c.browserCtx.Done():
			}
		}()
		defer func() { finished.Store(true); close(done) }()
	}
	return c.raw.Call(ctx,session,method,params)
}
func(c *diagnosticCDPClient) probes(session string){
	var wg sync.WaitGroup
	for _,x:=range []struct{s,m string}{{"","Browser.getVersion"},{session,"Page.getFrameTree"},{session,"Runtime.getIsolateId"}}{
		wg.Add(1)
		go func(x struct{s,m string}){defer wg.Done();ctx,cancel:=context.WithTimeout(c.browserCtx,2*time.Second);defer cancel();_,_=c.raw.Call(ctx,x.s,x.m,struct{}{})}(x)
	}
	wg.Wait()
}