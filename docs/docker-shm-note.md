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
