# P0 真实 DOM 取证报告 — 通知页 + 主页 tab（2026-08-03）

> 取证方式：agent-browser 接真实 cloakbrowser（CDP 9222），登录账号：一画一话
> 环境：RPi 4 / DietPi / 1G 内存（zram 已启用）/ viewport 780px
> 取证期间 MCP 浏览器空闲关闭一次（agent-browser 取证不算 MCP 活动），改用 create_browse_session 保活后完成

## 一、通知入口（explore 侧栏）

- 入口链接：`a[href="/notification"]`（侧栏内 class=`link-wrapper bottom-channel`，文本"通知"）
- badge 容器：`div.badge-container`（内含 svg.reds-icon + 未读数；当前无未读时无数字）
- 进入方式：真实点击入口 → URL=/notification

## 二、通知页（/notification）

- 页面容器：`.notification-page`（class 含 `ai-mode`）
- tab 栏：`.reds-sticky-box.sticky-tab` → `.reds-tabs-list`（tertiary left）
- **3 个 tab**：`.reds-tab-item.tab-item`（active 项加 `.active`）
  - `评论和@`（默认 active）
  - `赞和收藏`
  - `新增关注`
- tab 切换：点击 `.reds-tab-item`（非 active 的）

## 三、通知 item（.tabs-content-container .container）

### 评论和@（mentions）— 最完整
```
div.container
├── a.user-avatar（链接用户主页 /user/profile/<id>?channelTabId=mentions&xsec_token=...）
│   └── img.avatar-item.user-avatar
└── div.main
    ├── div.info
    │   ├── div.user-info
    │   │   ├── a（昵称，链接用户主页）
    │   │   └── span.user-tag（"你的好友"）
    │   ├── div.interaction-hint
    │   │   ├── span（"评论了你的笔记"）
    │   │   └── span.interaction-time（"06-09"）
    │   └── div.interaction-content（评论内容）
    └── div.actions
        ├── div.action-reply（回复按钮：svg use #chat + div.action-text"回复"）
        └── div.action-like（点赞按钮）
```

**点赞按钮状态判定（重要）**：
```
div.action-like
└── span.like-wrapper.like-active   ← like-active 类恒存在，不可靠！
    ├── span.like-lottie
    ├── svg.reds-icon.like-icon
    │   └── use xlink:href="#like"  ← 真实状态：#like=未赞 / #liked=已赞
    └── span.count
```
**与 feed 一致：状态看 svg use href，不看 like-active 类**

**回复区域**（点击 .action-reply 后展开）：
```
div.input-wrapper
├── textarea.comment-input（placeholder="回复 青史拾页"）← textarea 结构（非 contenteditable）
└── div.input-buttons
    └── div.submit（text=发送）
```

### 赞和收藏（likes）
- item 结构简化：头像 + 昵称 + `.interaction-hint`（"赞了你的笔记"）+ `.interaction-time`
- **无** .user-tag / .interaction-content / .action-reply / .action-like（无互动按钮）
- 实测 20 条

### 新增关注（connections）
- item：头像 + 昵称 + `.interaction-hint`（"开始关注你了"）+ `.interaction-time`
- **无**互动按钮

## 四、个人主页 tab（/user/profile/<自己的ID>）

- tab 容器：`.tertiary.center.reds-tabs-list`
- **3 个 tab**：`.reds-tab-item.sub-tab-list`（active 加 `.active`）
  - `笔记`（默认 active，显示"笔记・2"）
  - `收藏`
  - `点赞`
- 用户信息：`.user-info`（昵称 .user-nickname、小红书号、关注/粉丝数）

## 五、选择器草案（draft，P0 不改 ui_selectors.go）

| 用途 | 选择器 |
|---|---|
| 通知入口 | `a[href="/notification"]` |
| 通知 tab | `.reds-tab-item.tab-item`（active=`.active`，文本匹配 评论和@/赞和收藏/新增关注）|
| 通知 item | `.tabs-content-container .container` |
| 头像/用户链接 | `.container .user-avatar`（a，解析 userId+xsec_token）|
| 昵称 | `.container .user-info a` |
| hint | `.container .interaction-hint span`（首个）|
| 时间 | `.container .interaction-time` |
| 评论内容 | `.container .interaction-content`（仅 mentions）|
| 回复按钮 | `.container .action-reply`（仅 mentions）|
| 点赞按钮 | `.container .action-like`（仅 mentions）|
| 点赞状态 | `.action-like svg use` xlink:href #like/#liked |
| 回复输入框 | `textarea.comment-input` |
| 发送按钮 | `.input-buttons .submit` |
| 主页 tab | `.reds-tab-item.sub-tab-list`（文本 笔记/收藏/点赞）|

## 六、风险提示

- **进通知 tab 会清未读**（list_notifications 语义确认：查看即清，工具描述需披露）
- 点赞取证会改变状态（先存初始值，恢复用 #like/#liked 反向点击）
- 回复输入框是 textarea（与 feed 评论的 contenteditable 不同，选择器必须分开）
- 通知 item 的 xsec_token 在头像链接 URL 中（跳转用户主页用）
