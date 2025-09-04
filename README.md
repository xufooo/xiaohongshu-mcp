# xiaohongshu-mcp

MCP for 小红书/xiaohongshu.com。

- 我的博客文章：[haha.ai/xiaohongshu-mcp](https://www.haha.ai/xiaohongshu-mcp)


## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=xpzouying/xiaohongshu-mcp&type=Timeline)](https://www.star-history.com/#xpzouying/xiaohongshu-mcp&Timeline)


**主要功能**

1. 登录。第一步必须，小红书需要进行登录。
2. 发布图文。目前只支持发布图文，后续支持更多的发布功能。
3. 获取推荐列表。
4. 搜索内容。根据关键词搜索小红书内容。

**小红书基础运营知识**

- **标题：（非常重要）小红书要求标题不超过 20个字**
- 当前只支持图文发送：从推荐的角度看，图文的流量会比纯文字的更好。
- （低优先级）可以考虑视频和纯文字的支持。1. 个人感觉这两种会大大增加运营的复杂度；2. 这两类在我的使用场景的价值较低。
- Tags：马上支持。
- 根据本人实操，小红书每天的发帖量应该是 **50 篇**。
- **（非常重要）小红书的同一个账号不允许在多个网页端登录**，如果你登录了当前 xiaohongshu-mcp 后，就不要再在其他的网页端登录该账号，否则就会把当前 MCP 的账号“踢出登录”。你可以使用移动 App 端进行查看当前账号信息。

**风险说明**

1. 该项目是在自己的另外一个项目的基础上开源出来的，原来的项目稳定运行一年多，没有出现过封号的情况，只有出现过 Cookies 过期需要重新登录。
2. 我是使用 Claude Code 接入，稳定自动化运营数周后，验证没有问题后开源。

该项目是基于学习的目的，禁止一切违法行为。

**实操结果**

第一天点赞/收藏数达到了 999+，

<img width="386" height="278" alt="CleanShot 2025-09-05 at 01 31 55@2x" src="https://github.com/user-attachments/assets/4b5a283b-bd38-45b8-b608-8f818997366c" />

<img width="350" height="280" alt="CleanShot 2025-09-05 at 01 32 49@2x" src="https://github.com/user-attachments/assets/4481e1e7-3ef6-4bbd-8483-dcee8f77a8f2" />

一周左右的成果

<img width="1840" height="582" alt="CleanShot 2025-09-05 at 01 33 13@2x" src="https://github.com/user-attachments/assets/fb367944-dc48-4bbd-8ece-934caa86323e" />


## 1. 使用教程

### 1.1. 登录

第一次需要手动登录，需要保存小红书的登录状态。

运行

```bash
go run cmd/login/main.go
```

### 1.2. 启动 MCP 服务

启动 xiaohongshu-mcp 服务。

```bash

# 默认：无头模式，没有浏览器界面
go run .

# 非无头模式，有浏览器界面
go run . -headless=false
```

## 1.3. 验证 MCP

```bash
npx @modelcontextprotocol/inspector
```

![运行 Inspector](./assets/run_inspect.png)

运行后，打开红色标记的链接，配置 MCP inspector，输入 `http://localhost:18060/mcp` ，点击 `Connect` 按钮。

![配置 MCP inspector](./assets/inspect_mcp.png)

按照上面配置 MCP inspector 后，点击 `List Tools` 按钮，查看所有的 Tools。

## 1.4. 使用 MCP 发布

### 检查登录状态

![检查登录状态](./assets/check_login.gif)

### 发布图文

示例中是从 https://unsplash.com/ 中随机找了个图片做测试。

![发布图文](./assets/inspect_mcp_publish.gif)

### 搜索内容

使用搜索功能，根据关键词搜索小红书内容：

![搜索内容](./assets/search_result.png)

## 2. MCP 客户端接入

本服务支持标准的 Model Context Protocol (MCP)，可以接入各种支持 MCP 的 AI 客户端。

📖 **详细接入指南**：[MCP_README.md](./MCP_README.md)

### 2.1. 快速开始

```bash
# 启动 MCP 服务
go run .

# 使用 Claude Code CLI 接入
claude mcp add --transport http xiaohongshu-mcp http://localhost:18060/mcp
```

### 2.2. 支持的客户端

- ✅ **Claude Code CLI** - 官方命令行工具
- ✅ **Claude Desktop** - 桌面应用
- ✅ **Cursor** - AI 代码编辑器
- ✅ **VSCode** - 通过 MCP 扩展支持
- ✅ **MCP Inspector** - 调试工具
- ✅ 其他支持 HTTP MCP 的客户端

### 2.3. 可用 MCP 工具

- `check_login_status` - 检查登录状态
- `publish_content` - 发布图文内容
- `list_feeds` - 获取推荐列表
- `search_feeds` - 搜索小红书内容（前提：用户已登录）

### 2.4. 使用示例

使用 Claude Code 发布内容到小红书：

```
帮我写一篇帖子发布到小红书上，
配图为：https://cn.bing.com/th?id=OHR.MaoriRock_EN-US6499689741_UHD.jpg&w=3840
图片是："纽西兰陶波湖的Ngātoroirangi矿湾毛利岩雕（© Joppi/Getty Images）"

使用 xiaohongshu-mcp 进行发布。
```

![claude-cli 进行发布](./assets/claude_push.gif)

**发布结果：**

<img src="./assets/publish_result.jpeg" alt="xiaohongshu-mcp 发布结果" width="400">
