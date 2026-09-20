# Docker /dev/shm note

Chromium uses shared memory heavily. Docker's default `/dev/shm` size is often
64 MB, which can be too small for browser automation and may cause Chromium to
crash, hang, or fail to render pages.

When running this project in Docker, set a larger shared memory size for the
container. For example:

```yaml
services:
  xiaohongshu-mcp:
    shm_size: "1gb"
```

Or with `docker run`:

```bash
docker run --shm-size=1g ...
```

The project no longer relies on Chromium's `--disable-dev-shm-usage` flag by
default because the primary deployment target is Raspberry Pi bare metal, where
Docker's small default `/dev/shm` limit does not apply. Docker deployments should
configure `shm_size` explicitly instead.

> **Fact check（2026-09-20 修正）**：上面这段的"不再依赖 `--disable-dev-shm-usage`"
> 与实际行为不符。go-rod v0.116.2 的 `launcher.New()` 默认仍然带
> `--disable-dev-shm-usage`，本仓库没有任何地方 `Delete` 它。也就是说：
> 该 flag 目前**始终生效**，Docker 里的 `/dev/shm` 压力因此被绕开。
> 结论：**不要删除这个 flag**；`shm_size` 仍然值得显式配置，但它不是
> 当前不出问题的原因。`docker/docker-compose.yml` 已补 `shm_size: "256m"`
> （1GB 设备给满 1GB 会挤掉浏览器自身内存）。
>
> 证据：`go-rod/rod@v0.116.2 lib/launcher/launcher.go` 的 `New()` 默认 flags；
> 本仓库 `grep -rn "disable-dev-shm-usage"` 只有文档与测试字符串命中。

