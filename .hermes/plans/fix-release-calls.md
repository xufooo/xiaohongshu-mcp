修一个编译错误：

## ISSUE: [service.go:890] browserManager.Release 无返回值

`browserManager.Release(page)` 无返回值，但有两处写了 `_ = s.browserManager.Release(page)`。

修复：去掉 `_ = ` 前缀，改成：
```go
s.browserManager.Release(page)
```

涉及两处：service.go:890 和 service.go:894。

不要改动其他代码，不要扩展 scope。
