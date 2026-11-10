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
- [部署](#deployment)
- [测试](#testing)

<a id="introduction"></a>
## 简介

`ai` 包用于构建对话式的 AI 助手：模型边想边答，需要时调用应用提供的工具，回答以[服务端推送](sse.md)实时显示给用户。

agent 本身由 [trpc-agent-go](https://github.com/trpc-group/trpc-agent-go) 提供：模型、工具、提示词、记忆、多 agent 协作都直接使用它的 API，见[它的文档](https://trpc-group.github.io/trpc-agent-go/)。`ai` 包补上 Web 应用还需要的部分：

- **后台运行**：一次回答可能持续几分钟，不受 HTTP 请求的时限约束。用户刷新页面或网络断开后，生成过程不受影响，重新连接后接着收到剩余的内容。
- **中途插话**：模型还在查资料时，用户可以继续发消息，不需要等它答完，也不会打断它。
- **审批**：退款、发布这类操作，模型调用工具后先停下，由人确认后才执行。审批可以在几小时后、在另一个进程中进行。
- **事件流**：把运行过程转换成一组 JSON 事件，用 SSE 推给客户端，并生成 OpenAPI 文档。

本章为博客的编辑后台加一个写作助手：它可以搜索已有文章，帮编辑发布草稿。发布之前需要编辑确认。

<a id="preparing-tables"></a>
## 准备数据表

对话的消息保存在数据库中，模型每次回答时都会读出完整的历史。先[发布](migrations.md#publishing-framework-migrations)这些表的迁移：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "ai", FS: ai.Migrations()},
))
```

这些表的名称都以 `agent_` 开头，结构由 trpc-agent-go 的 PostgreSQL 会话存储决定。目前只支持 PostgreSQL。

<a id="assembling"></a>
## 装配

助手由三部分组成：模型、带工具的 agent，以及运行它的 `ai.Runs`。在 `internal/bootstrap/app.go` 的 `open` 中创建它们：

```go
ai.SetLogger(slog.Default())

sessions, err := aipg.NewSessions(cfg.Database.DSN) // aipg 是 github.com/ryan053857/mygoravel/ai/postgres
if err != nil {
    return nil, err
}

publish := assistant.PublishTool(articles)
agent := llmagent.New("writer",
    llmagent.WithModel(ai.NewModel(cfg.AI)),
    llmagent.WithInstruction("你是博客的写作助手，帮编辑查找文章和发布草稿。回答简洁。"),
    llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}),
    llmagent.WithTools([]tool.Tool{assistant.SearchTool(articles), publish}),
)
runs := ai.NewRuns("blog", agent, sessions, ai.RequireApproval(publish))
stack.MustAdd("ai", func(context.Context) error {
    return errors.Join(runs.Close(), sessions.Close())
})
```

- `ai.SetLogger` 让 trpc-agent-go 的日志写进应用的日志，带 `component=ai` 字段，可以[单独设置级别](logging.md#component-levels)。
- `aipg.NewSessions` 打开自己的连接池，不和 `*bun.DB` 共用。
- `ai.RequireApproval` 列出需要审批的工具，见[审批](#approval)。
- `runs.Close` 取消还在进行的运行。它登记在数据库之后，所以会先于数据库关闭。

工具是普通的 Go 函数，用 trpc-agent-go 的 `function.NewFunctionTool` 包装，参数和返回值按 JSON 编码，字段说明会告诉模型如何使用它：

```go
type SearchArgs struct {
    Query string `json:"query" jsonschema:"description=搜索关键词"`
}

func SearchTool(articles *article.Service) tool.CallableTool {
    return function.NewFunctionTool(func(ctx context.Context, args SearchArgs) ([]article.Summary, error) {
        return articles.Search(ctx, args.Query, 10)
    }, function.WithName("search_articles"), function.WithDescription("按关键词搜索已发布的文章"))
}
```

<a id="configuring-models"></a>
### 配置模型

`ai.ModelConfig` 描述一个 OpenAI 兼容接口的模型。DeepSeek、通义千问、Kimi、OpenAI 等都提供这种接口。把它加进应用配置：

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

Claude、Gemini 等其他接口的模型直接使用 trpc-agent-go 的对应包创建，`ai` 包的其余部分不受影响。

<a id="conversation-api"></a>
## 对话接口

每个对话用一个 ID 标识，由客户端生成，例如一个 UUID。对话属于当前登录的编辑，`Runs` 的方法都以主体 ID 和对话 ID 定位它，一个编辑无法读取其他人的对话。

接口分成两个：发消息的 `POST`，和接收回答的 `GET` 事件流。客户端先打开事件流，再发消息：

```go
type MessageInput struct {
    Conversation string `path:"conversation" format:"uuid"`
    Body         struct {
        Text string `json:"text" minLength:"1" maxLength:"4000"`
    }
}

type StreamInput struct {
    Conversation string `path:"conversation" format:"uuid"`
}

// POST /assistant/{conversation}/messages
func (h *Handlers) Send(ctx context.Context, in *MessageInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    _, err := h.runs.Send(ctx, sub.ID, in.Conversation, model.NewUserMessage(in.Body.Text))
    return nil, err
}

// GET /assistant/{conversation}/events
func (h *Handlers) Events(ctx context.Context, in *StreamInput) (*huma.StreamResponse, error) {
    sub, _ := identity.FromContext(ctx)
    run, ok := h.runs.Get(sub.ID, in.Conversation)
    if !ok {
        return nil, huma.Error404NotFound("no active run")
    }
    return rest.SSE(func(s *httpx.Stream) error { return ai.Stream(s, run) }), nil
}
```

事件流用 `rest.RegisterSSE` 注册，`ai.Events` 描述各个事件，用于生成 OpenAPI 文档：

```go
rest.RegisterSSE(api, op, ai.Events, func() func(context.Context, *StreamInput) (*huma.StreamResponse, error) {
    return h.Events
})
```

`ai.Stream` 依次推送下面的事件，最后一个总是 `done`：

| 事件 | 数据 | 含义 |
| --- | --- | --- |
| `text` | `{"text"}` | 回答的一段增量文本 |
| `reasoning` | `{"text"}` | 思考过程的一段增量文本，只有推理模型会发出 |
| `user` | `{"text"}` | 一条插话进入了对话 |
| `tool_call` | `{"id", "name", "arguments"}` | 模型调用了工具 |
| `tool_result` | `{"id", "name", "content"}` | 工具返回了结果 |
| `error` | `{"message"}` | 运行出错，例如模型服务不可用 |
| `done` | `{"pending", "canceled"}` | 运行结束，`pending` 是等待审批的调用 |

`Runs.Send` 在后台开始一次运行后立即返回，运行不会因为请求结束而停止。它也不受 [`httpx.Timeout`](middleware.md#request-timeout) 的影响，助手的路由和其他 API 装在同一组中间件下即可。

需要停止生成时，调用 `run.Cancel()`。已经生成的内容保留在对话中，事件流以 `canceled` 为 `true` 的 `done` 结束。

<a id="steering"></a>
### 中途插话

对话正在运行时，`Runs.Send` 不会开始新的运行，而是把消息插入当前这次运行：

- 模型正在调用工具时，消息在这批工具调用结束后、模型下一次思考之前进入对话。模型会同时看到工具的结果和这条新消息。
- 模型已经在给出最终回答时，它先答完，然后接着回答这条消息。

两种情况下，消息都不会打断模型，也不会丢失。消息进入对话时，事件流中出现一个 `user` 事件，前端可以在这时把它显示在对话中。

<a id="reconnecting"></a>
### 断线重连

每个事件都带有 ID。浏览器的 `EventSource` 断线后会自动重连，并在 `Last-Event-ID` 请求头中带上最后收到的 ID，`ai.Stream` 从它之后接着推送。

运行结束后，`Runs.Get` 还会在 1 分钟内返回它，让断线的客户端取得最后的事件，用 `ai.KeepFinished` 可以修改这个时长。更早的对话内容从会话存储中读取，用来渲染对话历史：

```go
sess, err := sessions.GetSession(ctx, session.Key{AppName: "blog", UserID: sub.ID, SessionID: id})
```

<a id="approval"></a>
## 审批

`ai.RequireApproval` 列出的工具，模型调用时不会执行。运行在其他工具执行完后停下，`done` 事件的 `pending` 列出等待审批的调用：

```json
{"pending": [{"id": "call_0", "name": "publish_article", "arguments": "{\"id\": \"0193...\"}"}]}
```

编辑确认后，用 `Runs.Approve` 执行这个调用，结果告诉模型，对话在后台继续运行；`Runs.Reject` 把拒绝的理由告诉模型：

```go
// POST /assistant/{conversation}/calls/{call}/approve
func (h *Handlers) Approve(ctx context.Context, in *CallInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    _, err := h.runs.Approve(ctx, sub.ID, in.Conversation, in.Call)
    return nil, err
}
```

待审批的调用保存在对话中，所以审批可以在任何时候、在任何一个实例上进行。页面重新打开时，用 `runs.Pending` 读出它们，重新显示确认按钮。

模型一次可能调用多个需要审批的工具。每个调用分别决定：只剩最后一个时，`Approve` 和 `Reject` 才继续运行，并返回这次运行；在此之前它们返回 `nil`。

对话等待审批期间，`Send` 返回 `ai.ErrAwaitingApproval`（`409`），因为模型在得到调用结果之前不能回答新问题。等待审批前已经插入、还没有进入对话的消息，在审批之后的运行中送达。

<a id="pages"></a>
## 在页面中使用

服务端渲染的页面用 `web.SSE` 推送 HTML 片段，`run.Events` 返回和 `ai.Stream` 相同的事件，由你渲染成组件：

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

<a id="deployment"></a>
## 部署

- **单实例**：运行和它的事件只保存在启动它的进程中。部署多个实例时，同一个对话的请求要落在同一个实例上，例如在负载均衡上按对话 ID 做粘性路由。审批不受这个限制。
- **停机**：进程停止时，进行中的运行被取消，已经生成的消息保留在对话中，用户可以继续对话。
- **后台任务**：不需要实时显示的 AI 工作（例如生成摘要、给文章打标签）交给[队列](queue.md)执行，在处理函数中直接调用 trpc-agent-go 的 `runner.Run`。队列任务[至少执行一次](queue.md#introduction)，这些工具需要能安全地重复执行。

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
