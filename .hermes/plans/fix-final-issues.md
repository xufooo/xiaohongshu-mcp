修 Codex 发现的 4 个 ISSUE，保持简洁：

## ISSUE 1 (P0): [browse_session.go:1156] currentURL 声明未使用

删掉 `currentURL := pageState.URL` 这一行。直接用 `pageState.URL` 和 `pageState.ReadyState` 做检查。

同时为 P1 的 URL 验证问题，加一行简单的 URL 检查：
```go
if pageState.URL == "" || strings.HasPrefix(pageState.URL, "about:") {
    return ReuseCheck{
        Status:          SessionNotReady,
        LastError:       "页面URL异常",
        HealthCheckedAt: checkedAt,
        Ready:           false,
    }
}
```

## ISSUE 2 (P1): [browse_session.go:1159] 登录失效/验证码页被误判为 ready

URL 空/about:blank 的检查已覆盖（上面的修复）。其他风险由具体的 session 操作工具（session_search 等）在运行时检测，不在 create 时做。

## ISSUE 3 (P1): [service.go:923] CheckReusable 到 Renew 之间的竞态

在 service.go 的 tryReuseSession 中，CheckReusable 返回 SessionReady 后再调一次简单的非阻塞检查：
```go
case xiaohongshu.SessionReady:
    // 再次检查 opToken，防止并发关闭
    if check2 := session.CheckReusable(ctx); check2.Status != xiaohongshu.SessionReady {
        return &xiaohongshu.CreateBrowseSessionResult{
            Outcome:           "blocked",
            RecommendedAction: "retry",
            Status: xiaohongshu.BrowseSessionStatusInfo{
                Status:    check2.Status,
                Ready:     false,
                LastError: check2.LastError,
            },
        }
    }
    info = session.Renew()
```

## ISSUE 4 (P2): [browse_session.go:65] 未使用的结构体字段

不用管，P2 不修。

不改其他代码，不要扩展 scope。
