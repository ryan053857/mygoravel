# AI 助手

- [简介](#introduction)
- [准备数据表](#preparing-tables)
- [装配](#assembling)
    - [配置模型](#configuring-models)
- [对话接口](#conversation-api)
    - [中途插话](#steering)
    - [断线重连](#reconnecting)
- [审批](#approval)
- [在页面中使用](#pages)
- [图片、语音和向量](#media)
- [决策](#decisions)
- [部署](#deployment)
    - [多个实例](#multiple-instances)
- [测试](#testing)

<a id="introduction"></a>
## 简介

`ai` 包用于构建对话式的 AI 助手：模型边想边答，需要时调用应用提供的工具，回答以[服务端推送](sse.md)实时显示给用户。

agent 本身由 [trpc-agent-go](https://github.com/trpc-group/trpc-agent-go) 提供：模型、工具、提示词、记忆、多 agent 协作都直接使用它的 API，见[它的文档](https://trpc-group.github.io/trpc-agent-go/)。`ai` 包补上 Web 应用还需要的部分：

- **后台运行**：一次回答可能持续几分钟，不受 HTTP 请求的时限约束。用户刷新页面或网络断开后，生成过程不受影响，重新连接后接着收到剩余的内容。
- **中途插话**：模型还在查资料时，用户可以继续发消息，不需要等它答完，也不会打断它。
- **审批**：退款、发布这类操作，模型调用工具后先停下，由人确认后才执行。审批可以在几小时后、在另一个进程中进行。
- **前端协议**：实现 Vercel AI SDK 的 UI 消息流协议，前端直接使用 `useChat`。
- **图片、语音和向量**：生成图片、合成和识别语音、计算向量、重排，接口统一，测试时有替身。
- **决策**：用专门的决策模型做分类、路由和打分，得到概率而不是文本。

本章为博客的编辑后台加一个写作助手：它可以搜索已有文章，帮编辑发布草稿。发布之前需要编辑确认。

<a id="preparing-tables"></a>
## 准备数据表

对话的消息保存在数据库中，模型每次回答时都会读出完整的历史。先[发布](migrations.md#publishing-framework-migrations)这些表的迁移：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "ai", FS: ai.Migrations()},
))
```

这些表的名称都以 `agent_` 开头，PostgreSQL、MySQL 和 SQLite 都支持，`migratecmd.Publish` 按传入的驱动选择对应的迁移。表结构与 trpc-agent-go 各数据库的会话存储完全一致，框架的测试会在每次升级 trpc-agent-go 时检查这一点。

<a id="assembling"></a>
## 装配

助手由三部分组成：模型、带工具的 agent，以及运行它的 `ai.Runs`。在 `internal/bootstrap/app.go` 的 `open` 中创建它们：

```go
ai.SetLogger(slog.Default())

sessions, err := aipg.NewSessions(db) // aipg 是 github.com/ryan053857/mygoravel/ai/postgres
if err != nil {
    return nil, err
}

publish := assistant.PublishTool(article.NewPublish(db))
agent := llmagent.New("writer",
    llmagent.WithModel(ai.NewModel(cfg.AI)),
    llmagent.WithInstruction("你是博客的写作助手，帮编辑查找文章和发布草稿。回答简洁。"),
    llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}),
    llmagent.WithTools([]tool.Tool{assistant.SearchTool(article.NewSearch(db)), publish}),
)
runs := ai.NewRuns("blog", agent, sessions, ai.RequireApproval(publish))
stack.MustAdd("ai", func(context.Context) error {
    return errors.Join(runs.Close(), sessions.Close())
})
```

- `ai.SetLogger` 让 trpc-agent-go 的日志写进应用的日志，带 `component=ai` 字段，可以[单独设置级别](logging.md#component-levels)。
- 对话存储按数据库选择包：`ai/postgres`、`ai/mysql` 或 `ai/sqlite`，用法相同，参数都是应用的 `*bun.DB`，和数据库驱动 `database/postgres` 等一一对应，应用只链接自己用的那个。PostgreSQL 和 MySQL 直接使用应用的连接池；SQLite 对同一个数据库文件另开一组连接（trpc-agent-go 关闭存储时会关闭它的连接），不支持内存数据库。`sessions.Close` 不会关闭应用的数据库。
- `ai.RequireApproval` 列出需要审批的工具，见[审批](#approval)。
- `runs.Close` 取消还在进行的运行。它登记在数据库之后，所以会先于数据库关闭。

工具是普通的 Go 函数，用 trpc-agent-go 的 `function.NewFunctionTool` 包装，参数和返回值按 JSON 编码，字段说明会告诉模型如何使用它：

```go
type SearchArgs struct {
    Query string `json:"query" jsonschema:"description=搜索关键词"`
}

func SearchTool(search *article.Search) tool.CallableTool {
    return function.NewFunctionTool(func(ctx context.Context, args SearchArgs) ([]article.Summary, error) {
        return search.Handle(ctx, args.Query, 10)
    }, function.WithName("search_articles"), function.WithDescription("按关键词搜索已发布的文章"))
}
```

<a id="configuring-models"></a>
### 配置模型

`ai.ModelConfig` 描述 OpenAI 兼容接口上的一个模型，DeepSeek、通义千问、Kimi、OpenAI 等都提供这种接口。它只是一个普通的结构体，`ai.NewModel` 按传进来的值创建模型，不关心这些值从哪里来。

模型固定时，把它加进应用配置：

```go
type Config struct {
    // ...
    AI ai.ModelConfig `yaml:"ai"`
}
```

```yaml
ai:
  model: deepseek-chat
  base_url: https://api.deepseek.com
  api_key: ${DEEPSEEK_API_KEY}
```

如果模型由管理员在后台维护，或者让用户在对话中挑选，就不在启动时固定模型，改用 `llmagent.WithModelSelector`。它在每次调用模型之前执行，从应用自己的数据中查出这一次使用的模型：

```go
agent := llmagent.New("writer",
    llmagent.WithModelSelector(func(ctx context.Context, inv *agent.Invocation) (model.Model, error) {
        p, err := settings.WriterModel(ctx, inv.Session.UserID) // 应用自己的表和规则
        if err != nil {
            return nil, err
        }
        return ai.NewModel(ai.ModelConfig{Model: p.Model, BaseURL: p.BaseURL, APIKey: p.APIKey}), nil
    }),
    // ...
)
```

`ai.NewModel` 不访问网络，各模型共用进程的 HTTP 连接池，每次调用时创建也没有额外开销。后台修改后，下一次调用模型就使用新的配置；审批后继续和插话后补跑的那一轮同样经过这里。

Claude、Gemini 等其他接口的模型直接使用 trpc-agent-go 的对应包创建，`ai` 包的其余部分不受影响。

<a id="conversation-api"></a>
## 对话接口

助手的接口使用 [Vercel AI SDK](https://ai-sdk.dev) 的 [UI 消息流协议](https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol)，前端直接使用它的 `useChat`（React、Vue、Svelte 都有），流式显示、工具调用、审批和断线续传都不需要自己实现。

每个对话用一个 ID 标识，由 `useChat` 生成。对话属于当前登录的编辑，`Runs` 的方法都以主体 ID 和对话 ID 定位它，一个编辑无法读取其他人的对话。

服务端提供四个接口：

```go
type ChatInput struct {
    Body ai.UIRequest
}

type ConversationInput struct {
    ID string `path:"id"`
}

type SteerInput struct {
    ID   string `path:"id"`
    Body struct {
        Text string `json:"text" minLength:"1" maxLength:"4000"`
    }
}

type HistoryOutput struct {
    Body []ai.UIMessage
}

// POST /assistant/chat：发送消息或审批结果，响应是这次回答的消息流。
func (h *Handlers) Chat(ctx context.Context, in *ChatInput) (*huma.StreamResponse, error) {
    sub, _ := identity.FromContext(ctx)
    run, err := h.runs.Submit(ctx, sub.ID, in.Body)
    if err != nil {
        return nil, err
    }
    return ai.UIStream(run), nil
}

// GET /assistant/chat/{id}/stream：断线后接上进行中的回答。
func (h *Handlers) Resume(ctx context.Context, in *ConversationInput) (*huma.StreamResponse, error) {
    sub, _ := identity.FromContext(ctx)
    run, err := h.runs.Get(ctx, sub.ID, in.ID)
    if err != nil {
        return nil, err
    }
    return ai.UIResume(run), nil
}

// POST /assistant/chat/{id}/steer：回答进行中插话。
func (h *Handlers) Steer(ctx context.Context, in *SteerInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    return nil, h.runs.Steer(ctx, sub.ID, in.ID, model.NewUserMessage(in.Body.Text))
}

// GET /assistant/chat/{id}/messages：打开页面时加载对话历史。
func (h *Handlers) History(ctx context.Context, in *ConversationInput) (*HistoryOutput, error) {
    sub, _ := identity.FromContext(ctx)
    messages, err := h.runs.UIMessages(ctx, sub.ID, in.ID)
    return &HistoryOutput{messages}, err
}
```

两个流式接口用 `rest.RegisterSSE` 注册，`ai.UIEvents` 描述消息流，用于生成 OpenAPI 文档：

```go
rest.RegisterSSE(api, op, ai.UIEvents, h.Chat)
```

前端按这四个接口配置 `useChat`：

```tsx
const { messages, sendMessage, addToolApprovalResponse } = useChat({
  id: conversationId,
  messages: initialMessages, // 来自 GET /assistant/chat/{id}/messages
  resume: true,
  transport: new DefaultChatTransport({
    api: '/api/v1/assistant/chat',
    headers: { Authorization: `Bearer ${token}` },
    // 历史保存在服务端，只发送最后一条消息
    prepareSendMessagesRequest: ({ id, messages }) => ({ body: { id, message: messages.at(-1) } }),
  }),
  sendAutomaticallyWhen: lastAssistantMessageIsCompleteWithApprovalResponses,
});
```

`Runs.Submit` 在后台开始一次运行后立即返回，运行不会因为请求结束而停止。它也不受 [`httpx.Timeout`](middleware.md#request-timeout) 的影响，助手的路由和其他 API 装在同一组中间件下即可。对话正在回答时再次提交，`Submit` 返回 `ai.ErrBusy`（`409`）。

需要停止生成时，调用 `run.Cancel()`。已经生成的内容保留在对话中，消息流以 `abort` 结束。

<a id="steering"></a>
### 中途插话

回答进行中，`useChat` 的 `sendMessage` 会等这次回答结束后才发送。要让模型在回答过程中就看到新消息，前端改为调用插话接口：

```ts
await fetch(`/api/v1/assistant/chat/${conversationId}/steer`, { method: 'POST', body: JSON.stringify({ text }), headers })
```

- 模型正在调用工具时，消息在这批工具调用结束后、模型下一次思考之前进入对话。模型会同时看到工具的结果和这条新消息。
- 模型已经在给出最终回答时，它先答完，然后接着回答这条消息。

两种情况下，消息都不会打断模型，也不会丢失。消息进入对话时，回答中出现一个 `steering-message` 片段，前端把它显示在对话中。对话没有在回答时，`Runs.Steer` 返回 `ai.ErrFinished`，前端改用 `sendMessage`。

<a id="reconnecting"></a>
### 断线重连

`useChat` 设置了 `resume: true` 时，页面打开和网络恢复后会请求 `GET {api}/{id}/stream`。回答还在进行时，`ai.UIResume` 从头重放它，`useChat` 用重放的内容重建这条回答；没有进行中的回答时返回 `204`。

已经完成的内容由 `Runs.UIMessages` 从对话存储中读出，作为 `useChat` 的初始消息。

<a id="approval"></a>
## 审批

`ai.RequireApproval` 列出的工具，模型调用时不会执行。运行在其他工具执行完后停下，消息流为每个调用发出 `tool-approval-request`，`useChat` 中这个工具片段的状态变为 `approval-requested`，前端显示确认按钮：

```tsx
{part.state === 'approval-requested' && (
  <>
    <button onClick={() => addToolApprovalResponse({ id: part.approval.id, approved: true })}>发布</button>
    <button onClick={() => addToolApprovalResponse({ id: part.approval.id, approved: false, reason: '暂不发布' })}>取消</button>
  </>
)}
```

所有调用都有了决定后，`useChat` 自动把它们提交给 `POST /assistant/chat`。`Runs.Submit` 对批准的调用执行工具，对拒绝的调用把理由告诉模型，然后对话继续运行，新的回答接在同一条消息后面。

待审批的调用保存在对话中，所以审批可以在任何时候、在任何一个实例上进行。页面重新打开时，`Runs.UIMessages` 返回的历史中这些调用仍然处于 `approval-requested` 状态。不使用 `useChat` 时，用 `Runs.Pending` 读出它们，用 `Runs.Approve` 和 `Runs.Reject` 逐个处理。

对话等待审批期间不能发送新消息，`Submit` 返回 `ai.ErrAwaitingApproval`（`409`），因为模型在得到调用结果之前不能回答新问题。等待审批前已经插入、还没有进入对话的消息，在审批之后的运行中送达。

<a id="pages"></a>
## 在页面中使用

服务端渲染的页面不使用 `useChat`，而是用 `web.SSE` 推送 HTML 片段。`run.Events` 依次返回运行中的事件（回答文本、工具调用、结束），由你渲染成组件：

```go
return web.SSE(w, r, func(s *web.Stream) error {
    for ev := range run.Events(s.Context(), s.LastEventID()) {
        var c templ.Component
        switch data := ev.Data.(type) {
        case ai.Text:
            if ev.Name != ai.EventText {
                continue
            }
            c = views.AnswerDelta(data.Text) // <hx-partial hx-target="#answer" hx-swap="beforeend">
        case ai.Done:
            c = views.Approvals(data.Pending)
        default:
            continue
        }
        if err := s.SendEvent(web.Event{ID: ev.ID}, c); err != nil {
            return nil
        }
    }
    return nil
})
```

<a id="media"></a>
## 图片、语音和向量

除了对话，`ai` 包还定义了五种能力，每种是一个只有一个方法的接口：

| 接口 | 方法 | 用途 |
| --- | --- | --- |
| `ai.Imager` | `Image(ctx, ai.ImageRequest) (ai.Media, error)` | 按描述生成图片，给出参考图时编辑或改绘 |
| `ai.Speaker` | `Speak(ctx, ai.SpeechRequest) (ai.Media, error)` | 把文字合成为语音 |
| `ai.Transcriber` | `Transcribe(ctx, ai.TranscriptionRequest) (string, error)` | 把语音识别为文字 |
| `ai.Embedder` | `Embed(ctx, texts []string) ([][]float32, error)` | 把文本转换为向量 |
| `ai.Reranker` | `Rerank(ctx, query string, docs []string, limit int) ([]ai.Ranked, error)` | 按与查询的相关度给文档排序 |

`ai.Media` 是图片或音频的内容和 MIME 类型。

`ai/openai` 通过 OpenAI 兼容接口实现这五种能力。`openai.New` 和 `ai.NewModel` 一样接收 `ai.ModelConfig`，不访问网络，可以在每次使用时按当时的配置创建。OpenAI、火山方舟、硅基流动、通义千问的兼容模式等都提供这类接口，但每家支持的能力不同，以厂商文档为准。

[OpenRouter](https://openrouter.ai) 使用 `ai/openrouter`，用法与 `ai/openai` 相同。它的语音和向量是 OpenAI 兼容接口，图片则使用 OpenRouter 自己的接口，所以单独提供一个包；`BaseURL` 可以不填。

`openai.Client` 的 `OpenAI()` 返回底层的 openai-go 客户端，已经设置好地址和密钥，可以用它调用厂商自己的其他接口。

为文章生成封面，存进[文件存储](filesystem.md)：

```go
img, err := openai.New(cfg.Cover).Image(ctx, ai.ImageRequest{
    Prompt: "为这篇文章画一张扁平风格的封面：" + post.Title,
    Aspect: ai.Landscape,
})
if err != nil {
    return err
}
file, err := files.Upload(ctx, owner, filesystem.FileInput{
    Filename:    "cover.png",
    ContentType: img.MIME,
    Size:        int64(len(img.Data)),
}, bytes.NewReader(img.Data))
```

- `Aspect` 是 `ai.Square`、`ai.Portrait` 或 `ai.Landscape`，`ai/openai` 按 OpenAI 的尺寸换算，尺寸规格不同的模型用 `openai.WithImageSizes` 指定，例如 `{ai.Square: "2048x2048"}`；`ai/openrouter` 换算为 1:1、2:3 和 3:2。
- 模型服务只返回临时链接时，`Image` 会下载图片，结果同样是图片内容。

朗读文章，以及把编辑的语音输入转成文字：

```go
audio, err := openai.New(cfg.Voice).Speak(ctx, ai.SpeechRequest{Text: post.Body, Voice: "alloy"})

text, err := openai.New(cfg.Dictation).Transcribe(ctx, ai.TranscriptionRequest{
    Audio:    ai.Media{Data: data, MIME: "audio/webm"}, // 浏览器 MediaRecorder 录的音
    Language: "zh",
})
```

计算向量时一次传入多段文本。有的厂商限制每批的数量，用 `openai.WithBatchSize` 让它分批请求；支持缩减维度的模型用 `openai.WithDimensions` 指定维度：

```go
vecs, err := openai.New(cfg.Embedding, openai.WithBatchSize(10)).Embed(ctx, []string{post.Title, post.Body})
```

关键词搜索找到的候选，可以用重排模型按语义重新排序，只把最相关的几篇交给模型或用户。`ai.RerankBy` 直接对任意类型的列表排序：

```go
posts, err = ai.RerankBy(ctx, openai.New(cfg.Rerank), query, posts, func(p article.Summary) string {
    return p.Title
}, 5)
```

重排不是 OpenAI 定义的接口，而是 Cohere 和 Jina 的 `POST {BaseURL}/rerank`，硅基流动、OpenRouter、vLLM 等都沿用了它，`ai/openai` 和 `ai/openrouter` 都按这个格式调用。Cohere 自己的地址填 `https://api.cohere.com/v2`。

生成图片和语音通常要十几秒到几十秒，不需要立即返回结果时，放进[队列](queue.md)执行。

<a id="decisions"></a>
## 决策

工单该分给哪个团队、评论是不是垃圾信息、问题有多严重，这类判断交给决策模型。它不生成文本，而是读一份材料，回答你定义的问题，给出概率。与让对话模型输出 JSON 相比，它快得多、便宜得多，结果也稳定，可以按概率设定阈值。

`ai.Decider` 回答三种问题：

| 问题 | 创建 | 回答 |
| --- | --- | --- |
| 是非题 | `ai.YesNo(name, instructions)`，可以用 `.Criteria(成立, 不成立)` 写明标准 | `Probability`：条件成立的概率；`Yes(threshold)` |
| 选择题 | `ai.Choice(name, instructions, options...)`，至少两个选项 | `Choice`：选中的值；`ProbabilityOf(value)` |
| 打分题 | `ai.Score(name, instructions, levels...)`，从低到高至少两级 | `Score`：按概率加权的级别序号；`Level()`、`Label()`：最可能的一级 |

给工单分流：

```go
res, err := decider.Decide(ctx, ai.DecisionRequest{
    Input: ticket, // 字符串按文本发送，结构体和 map 按 JSON 发送
    Questions: []ai.Question{
        ai.Choice("team", "应该由哪个团队处理？",
            ai.ChoiceOption{Value: "billing", Description: "支付、退款和账单"},
            ai.ChoiceOption{Value: "technical", Description: "产品故障和集成"}),
        ai.YesNo("page", "是否需要立即通知值班人员？").Criteria("核心功能不可用或资金受影响", "可以等工作时间处理"),
        ai.Score("severity", "这个问题有多严重？", ai.Levels("轻微", "部分受影响", "核心不可用")...),
    },
})
if err != nil {
    return err
}
team := res.Answers["team"].Choice
if res.Answers["page"].Yes(0.8) {
    // 通知值班
}
```

- 概率在不同模型之间不可比，阈值要用自己的样本为每个模型确定。
- 模型可能拒绝回答某个问题，这时回答的 `Refused` 为 true，`Yes` 返回 false，其余问题不受影响。
- `Images` 一起判断图片；给图片设置 `Name`，问题里就能用名字指代它，例如“front 中的商品是否破损”。

从业务数据中选一项时使用 `ai.DecideBy`，概率低于阈值或模型拒答时返回 false：

```go
team, ok, err := ai.DecideBy(ctx, decider, "哪个团队负责这个工单？", ticket.Body, teams,
    func(t Team) ai.ChoiceOption { return ai.ChoiceOption{Value: t.Slug, Description: t.Scope} }, 0.6)
if err == nil && !ok {
    team = fallback // 交给人工分配
}
```

决策接口有两种格式：

- **OpenAI**：`openai.Client` 调用 `/decisions`，模型是 `gpt-6-luna`。
- **System One**：`ai/systemone` 调用 `/systemone`。阿里云百炼的 BaseURL 填 `https://<工作空间>.cn-beijing.maas.aliyuncs.com/compatible-mode/v1`，TypeSafe 填 `https://api.typesafe.ai/v1`。`ai/openrouter` 也使用这种格式，OpenRouter 上 TypeSafe、Perplexity、Liquid、Cloudflare 等的决策模型，包括 `openai/gpt-6-luna-decisions`，都能直接使用。

```go
decider := systemone.New(ai.ModelConfig{Model: "decision-model-preview", BaseURL: bailianURL, APIKey: key})
```

各家模型支持的输入不同：百炼的 `decision-model-preview` 目前只看文本，TypeSafe 的 Jev 要求图片边长不小于 512 像素。

<a id="deployment"></a>
## 部署

- **停机**：进程停止时，进行中的运行被取消，已经生成的消息保留在对话中，用户可以继续对话。
- **后台任务**：不需要实时显示的 AI 工作（例如生成摘要、给文章打标签）交给[队列](queue.md)执行，在处理函数中直接调用 trpc-agent-go 的 `runner.Run`。队列任务[至少执行一次](queue.md#introduction)，这些工具需要能安全地重复执行。

<a id="multiple-instances"></a>
### 多个实例

一次运行在启动它的实例上执行。部署多个实例时，用 `ai.Cluster` 把各实例的 `Runs` 连起来，同一个对话的请求就可以落在任何一个实例上：

```go
runs := ai.NewRuns("blog", agent, sessions,
    ai.RequireApproval(publish),
    ai.Cluster(hub, locker),
)
```

`hub` 是[广播](broadcasting.md#creating-the-hub)的 Hub，`locker` 是[原子锁](locks.md)，都在 `open` 中创建，所有实例必须使用相同的后端，例如数据库或 Redis。

- 对话正在运行时，运行所在的实例持有这个对话的锁。其他实例上的 `Submit`、`Start` 因此返回 `ai.ErrBusy`，不会同时运行两次。
- 运行的事件每 50 毫秒批量广播一次。其他实例上的 `Runs.Get` 先向运行所在的实例要一份目前为止的快照，再接收之后的事件，返回的 `Run` 用法与本地的相同；`Steer` 和 `Cancel` 转发给运行所在的实例，`Steer` 等待它的回执。
- 运行所在的实例意外退出时，锁在 30 秒内过期；镜像 15 秒收不到消息就以一个 `error` 事件结束，浏览器重新续传时得到最新的状态。
- 广播使用数据库后端时，事件多一个轮询间隔（100 毫秒）的延迟。

审批读写的是对话存储，本来就不依赖实例。

<a id="testing"></a>
## 测试

`aitest.NewModel` 是一个按脚本回复的模型，不访问真实的模型服务。用它代替 `ai.NewModel`，就能测试工具调用、插话和审批的完整流程：

```go
m := aitest.NewModel(
    aitest.Calls(aitest.Call{Name: "search_articles", Args: assistant.SearchArgs{Query: "Go"}}),
    aitest.Text("找到 3 篇相关文章"),
)
```

模型依次给出这两个回复：先调用 `search_articles`，拿到结果后回答。`m.Requests()` 返回模型收到的请求，可以检查发给模型的历史和工具结果。会话存储使用 trpc-agent-go 的 `inmemory.NewSessionService()`。

图片、语音和向量也有对应的替身：

```go
img := aitest.NewImager()            // 每次返回 aitest.PNG
emb := aitest.NewEmbedder(1024)      // 相同的文本总是得到相同的向量
sp := aitest.NewSpeaker()            // 把朗读的文字当作音频内容返回
tr := aitest.NewTranscriber("你好")  // 依次返回预设的文字
rr := aitest.NewReranker()           // 按查询中的字符在文档中出现的比例排序
dc := aitest.NewDecider()            // 是非题 0.5，选择题选第一项，打分题最低一级
```

传入预设结果时按顺序返回，用完后再收到请求就返回错误；`Requests()` 返回收到的请求，`FailWith(err)` 让之后的请求失败，用于测试出错的处理。
