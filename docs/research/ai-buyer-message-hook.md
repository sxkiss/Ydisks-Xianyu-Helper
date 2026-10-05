# 调研：把买家消息接入 AI 自动回复（不影响原功能）

> 调研日期：2026-10-05 ｜ 方式：只读代码/数据库分析，未改动任何代码与配置

## 一、结论（TL;DR）

**AI 回复功能其实早就有了，而且已经接线、已经启用**，但它被一行硬编码的正则**锁死在"砍价场景"**：

```go
// internal/engine/ai.go:62-64
if !bargainMessageRe.MatchString(strings.ToLower(m.Text)) {
    return nil, nil   // 非砍价消息 → 直接跳过 AI
}
```

所以买家发来的**普通消息（咨询、问价、售后、催发货…）根本不会进 AI**，只会走
「关键词回复」→「默认回复」。这就是后台只看到"砍价 + 关键词"的原因——不是没接，是被门闸挡了。

**要接买家消息进 AI，必须改代码**（加一个通用 AI 分支），单靠后台配置做不到。

---

## 二、现有架构（已核实）

### 2.1 四级回复引擎（优先级从高到低）

`internal/engine/reply.go` → `resolve()`

| 优先级 | 来源 | 现状 |
|---|---|---|
| 1 | API 回复 | 未接线（nil） |
| 2 | **关键词回复** | ✅ 在用 |
| 3 | **AI 回复** | ⚠️ 在用但**仅砍价** |
| 4 | **默认回复** | ✅ 在用 |

> 注意：**关键词优先级高于 AI**。只要关键词命中，AI 就不会被调用。

### 2.2 消息入口链路（无需改动）

```
闲鱼 WebSocket (internal/xianyu/ws/client.go)
  → engine/dispatch.go  extractChatMessage()  解析消息
  → engine/message_dispatcher.go  dispatch()
      · markAndCheckDedup()      去重
      · scheduleDebouncedReply() 防抖（合并连续消息）
  → ReplyService.Handle() → resolve() 四级链
  → 发送 + 落库（chat_messages / default_reply_records）
```

**关键**：防抖、去重、发送、落库与"回复内容从哪来"完全解耦。新增回复来源**不需要碰**这条链路。

### 2.3 AI 回复实现

- 文件：`internal/engine/ai.go`（`AIReplierImpl`）
- 接线：`internal/engine/account.go:379` `NewAIReplier(...)` → `NewReplyService(..., aiReplier, ...)`
- 模型调用：OpenAI 兼容接口（go-openai），默认 `dashscope compatible-mode` + `qwen-plus`
- 提示词：`buildSystemPrompt(custom_prompts, 商品标题/价格/描述, 折扣上限, 金额上限, 砍价轮次, 自动改价开关)`
- 附带能力：`[[AUTO_PRICE:x.xx]]` 标记 → 30 分钟内可自动改价

### 2.4 配置现状（线上实例已核实）

`ai_reply_settings`（cookie_id=331540304）：`ai_enabled=1`、qwen-plus、折扣上限 10/100、轮次 3、自动改价=1。
`system_settings`：`ai_api_key` 已配置（106 字符，加密存储）。

取值链：`globalAIConfig()` → `ReadSensitiveSettingForAccount()` → `ReadSensitiveSetting(ownerID)` → `system_settings`。
**API Key 可读到，不是配置缺失问题。**

### 2.5 官方文档已明说这是"砍价专用"

`docs/wiki/AI、通知与运维.zh-CN.md`：
> "AI 用于处理买家的砍价消息。"
> 排障："AI 无回复：…且消息属于砍价场景。"

---

## 三、根因

| # | 根因 | 位置 | 影响 |
|---|---|---|---|
| 1 | **AI 有砍价门闸** | `ai.go:62-64` `bargainMessageRe` | 非砍价消息 100% 跳过 AI |
| 2 | 关键词优先级高于 AI | `reply.go` `resolve()` | 命中关键词时 AI 不参与（合理，保留） |
| 3 | API 回复层未接线 | `account.go:381` 传 nil | 优先级 1 空置（不影响目标） |

---

## 四、改造方案

### 推荐：新增「通用 AI 客服」作为 AI 内部 opt-in 分支

**原则：砍价 AI 一行不动，新增并行分支，默认关闭 → 零行为变化。**

#### 改动点

1. **`internal/engine/ai.go`** — `Reply()` 内分流：

   ```
   读取 cfg（ai_enabled）
     ├─ 命中 bargainMessageRe → 【现有砍价逻辑，原样保留】
     └─ 未命中 且 cfg.GeneralEnabled → 【新增通用客服逻辑】
            · system prompt 换"通用客服"版（商品信息+历史+自定义FAQ）
            · 禁止报价/改价（不产生 AIPriceQuoteProposal）
            · 命中转人工关键词 → 返回 nil（交回默认回复/人工）
     └─ 否则 → return nil（现状不变）
   ```

2. **`internal/db`** — `ai_reply_settings` 加列（幂等 migration，带默认值）：
   - `general_enabled INTEGER DEFAULT 0` — 通用 AI 开关（**默认关**）
   - `general_prompts TEXT DEFAULT ''` — 通用客服提示词
   - `handoff_keywords TEXT DEFAULT ''` — 转人工关键词
   - `general_cooldown_seconds INTEGER DEFAULT 0` — 同会话回复冷却

3. **`internal/application/settings`** — 读写新字段（沿用现有审计/加密）。

4. **前端**（settings 特性区）— 加：通用客服开关、提示词、转人工词、冷却秒数。

5. **`internal/engine/reply.go`** — **不需要改**。

#### 为什么安全

| 关注点 | 说明 |
|---|---|
| 原功能不变 | 砍价分支零改动；新分支默认关 |
| 优先级不变 | 关键词仍优先于 AI |
| 公共链路零改动 | 防抖/去重/发送/落库复用 |
| 可回滚 | 关开关即恢复；migration 只加列 |
| 安全 | 复用 netguard HTTP client；提示词禁放机密 |
| 可观测 | Source 标记区分（AI vs AI-通用） |

#### 安全阀（防 AI 乱答）

1. 只回文本消息（跳过图片/卡片/系统通知）
2. 转人工关键词直接不回复
3. 冷却 + 轮次上限，超限转人工
4. 默认关闭 + 单账号灰度
5. 通用分支永不输出 `[[AUTO_PRICE]]`

### 备选方案对比

| 方案 | 做法 | 评价 |
|---|---|---|
| A（推荐） | AI 内部分流 + opt-in 开关 | 改动最小、隔离最好 |
| B | resolve() 新增独立优先级 | 语义清晰但改动大 |
| C | AI 作为兜底（默认回复前） | ❌ 风险高，改变所有消息行为 |

---

## 五、工作量估算

后端 0.5–1 天 + 前端 0.5 天 + 测试灰度 0.5 天 ≈ **1.5–2 天**。

## 六、立即能做的（零代码）

1. 现有**砍价 AI 已启用**，可用真实砍价对话直接验证。
2. 高频咨询词（发货时间/怎么用/发票…）先配成**关键词回复**，立刻覆盖大部分咨询。
3. 通用 AI 分支上线后再做兜底。

## 附：关键文件索引

| 作用 | 路径 |
|---|---|
| 四级回复调度 | `internal/engine/reply.go` (resolve) |
| AI 回复实现（含砍价门闸） | `internal/engine/ai.go:54-64` |
| AI 接线 | `internal/engine/account.go:379-383` |
| 消息分发/防抖/去重 | `internal/engine/message_dispatcher.go` |
| 消息解析 | `internal/engine/dispatch.go` |
| 账号级 AI 配置表 | `ai_reply_settings` |
| 全局 AI 配置 | `system_settings` |
| 官方说明 | `docs/wiki/AI、通知与运维.zh-CN.md` |
