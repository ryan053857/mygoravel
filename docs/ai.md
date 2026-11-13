# AI 助手

- [简介](#introduction)
- [准备数据表](#preparing-tables)
- [装配](#assembling)
    - [配置模型](#configuring-models)
    - [按对话选择 agent](#choosing-agents)
- [对话接口](#conversation-api)
    - [停止回答](#stopping)
    - [中途插话](#steering)
    - [断线重连](#reconnecting)
    - [加入其他消息](#appending-messages)
- [审批](#approval)
- [使用应用自己的消息表](#own-history)
    - [实现 HistoryStore](#history-store)
    - [群聊中的 AI](#group-chat)
- [图片、语音和向量](#media)
- [决策](#decisions)
- [部署](#deployment)
    - [多个实例](#multiple-instances)
    - [在 worker 中运行](#running-on-workers)
- [测试](#testing)

<a id="introduction"></a>
## 简介

`ai/chat` 包用于构建对话式的 AI 助手：模型边想边答，需要时调用应用提供的工具，回答以[服务端推送](sse.md)实时显示给用户。

agent 本身由 [trpc-agent-go](https://github.com/trpc-group/trpc-agent-go) 提供：模型、工具、提示词、记忆、多 agent 协作都直接使用它的 API，见[它的文档](https://trpc-group.github.io/trpc-agent-go/)。`ai/chat` 补上 Web 应用还需要的部分：

- **后台运行**：一次回答可能持续几分钟，不受 HTTP 请求的时限约束。用户刷新页面或网络断开后，生成过程不受影响，重新连接后接着收到剩余的内容。
- **中途插话**：模型还在查资料时，用户可以继续发消息，不需要等它答完，也不会打断它。
- **审批**：退款、发布这类操作，模型调用工具后先停下，由人确认后才执行。审批可以在几小时后、在另一个进程中进行。
- **前端协议**：实现 Vercel AI SDK 的 UI 消息流协议，前端直接使用 `useChat`。
- **图片、语音和向量**：生成图片、合成和识别语音、计算向量、重排，接口统一，测试时有替身。
- **决策**：用专门的决策模型做分类、路由和打分，得到概率而不是文本。

后两项在 `ai` 包中，不依赖 `ai/chat`：只用到它们的应用不会链接 trpc-agent-go。

本章为博客的编辑后台加一个写作助手：它可以搜索已有文章，帮编辑发布草稿。发布之前需要编辑确认。

<a id="preparing-tables"></a>
## 准备数据表

对话的消息保存在数据库中，模型每次回答时都会读出完整的历史。先[发布](migrations.md#publishing-framework-migrations)这些表的迁移：

```go
migrationCmd.AddCommand(migratecmd.Publish(
    migrate.Source{Name: "ai", FS: chat.Migrations()},
))
```

这些表的名称都以 `agent_` 开头，表结构与 trpc-agent-go 的 PostgreSQL 会话存储完全一致，框架的测试会在每次升级 trpc-agent-go 时检查这一点。

<a id="assembling"></a>
## 装配

助手由三部分组成：模型、带工具的 agent，以及运行它的 `chat.Runs`。在 `internal/bootstrap/app.go` 的 `open` 中创建它们：

```go
chat.SetLogger(slog.Default())

sessions, err := chat.NewSessions(db)
if err != nil {
    return nil, err
}

publish := assistant.PublishTool(article.NewPublish(db))
agent := llmagent.New("writer",
    llmagent.WithModel(chat.NewModel(cfg.AI)),
    llmagent.WithInstruction("你是博客的写作助手，帮编辑查找文章和发布草稿。回答简洁。"),
    llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}),
    llmagent.WithTools([]tool.Tool{assistant.SearchTool(article.NewSearch(db)), publish}),
)
runs := chat.NewRuns("blog", agent, sessions, chat.RequireApproval(publish))
stack.MustAdd("ai", func(ctx context.Context) error {
    // 等进行中的回答说完，最多 1 分钟，之后取消剩下的
    drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
    defer cancel()
    return errors.Join(runs.Shutdown(drain), runs.Close(), sessions.Close())
})
```

- `chat.SetLogger` 让 trpc-agent-go 的日志写进应用的日志，带 `component=ai` 字段，可以[单独设置级别](logging.md#component-levels)。
- `chat.NewSessions` 把对话保存在应用的 PostgreSQL 中，直接使用应用的连接池，`sessions.Close` 不会关闭它。
- `chat.RequireApproval` 列出需要审批的工具，见[审批](#approval)。
- `runs.Shutdown` 让进程停止时进行中的回答说完，见[部署](#deployment)；`runs.Close` 取消剩下的运行并关闭 runner。它们登记在数据库之后，所以会先于数据库关闭。

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

`ai.ModelConfig` 描述 OpenAI 兼容接口上的一个模型，DeepSeek、通义千问、Kimi、OpenAI 等都提供这种接口。它只是一个普通的结构体，`chat.NewModel` 按传进来的值创建模型，不关心这些值从哪里来。

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
  max_retries: 2 # 可省略，默认就是 2；-1 表示不重试
```

模型请求和普通的 HTTP 请求一样，遇到暂时的失败会重试几次，仍然失败就返回错误，不会排队等待：

- 连接失败、`408`、`409`、`429` 和 `5xx` 时重试。服务端给出 `Retry-After`（1 分钟以内）就按它等待，否则指数退避：0.5 秒起，每次翻倍，最长 8 秒。
- 重试用完后返回错误。对话中，这次回答以 `error` 事件结束，用户可以重新发送；在[队列](queue.md)任务中调用模型时，失败由任务的重试策略接着处理。
- 模型已经开始流式返回内容后出错不会重试：已经推给用户的半段回答无法撤回，这次回答以错误结束。

`max_retries` 同样作用于[图片、语音和向量](#media)的请求。

如果模型由管理员在后台维护，或者让用户在对话中挑选，就不在启动时固定模型，改用 `llmagent.WithModelSelector`。它在每次调用模型之前执行，从应用自己的数据中查出这一次使用的模型：

```go
agent := llmagent.New("writer",
    llmagent.WithModelSelector(func(ctx context.Context, inv *agent.Invocation) (model.Model, error) {
        p, err := settings.WriterModel(ctx, inv.Session.UserID) // 应用自己的表和规则
        if err != nil {
            return nil, err
        }
        return chat.NewModel(ai.ModelConfig{Model: p.Model, BaseURL: p.BaseURL, APIKey: p.APIKey}), nil
    }),
    // ...
)
```

`chat.NewModel` 不访问网络，各模型共用进程的 HTTP 连接池，每次调用时创建也没有额外开销。后台修改后，下一次调用模型就使用新的配置；审批后继续和插话后补跑的那一轮同样经过这里。

Claude、Gemini 等其他接口的模型直接使用 trpc-agent-go 的对应包创建，`ai/chat` 的其余部分不受影响。

<a id="choosing-agents"></a>
### 按对话选择 agent

一个产品里常有多个助手，例如每个「AI 员工」有自己的模型、指令、工具和知识库，每个对话由其中一个负责。这时不在启动时固定 agent，改用 `chat.AgentFor` 按对话选出它，`NewRuns` 的 agent 传 `nil`：

```go
runs := chat.NewRuns("luway", nil, sessions,
    chat.AgentFor(func(ctx context.Context, c chat.Conversation) (agent.Agent, error) {
        emp, err := employees.ForConversation(ctx, c.ID) // 应用自己的表和规则
        if err != nil {
            return nil, err
        }
        return llmagent.New(emp.AgentName,
            llmagent.WithModel(chat.NewModel(emp.Model)),
            llmagent.WithInstruction(emp.Instruction),
            llmagent.WithTools(emp.Tools()),
            llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}),
        ), nil
    }),
    chat.RequireApprovalOf("refund_order"),
)
```

- 每一轮请求模型之前调用一次，后台修改员工的配置，下一轮就生效。创建 llmagent 不访问网络，开销很小；需要时也可以按配置的版本缓存。
- 同一个员工的 agent 名称要保持不变：它是消息的作者，trpc-agent-go 把其他名称的 agent 的回答当作其他参与者的话交给模型。
- 返回错误时这一轮失败，消息流以 `error` 事件结束。
- 工具由各个员工自己提供，创建 `Runs` 时还不存在，所以用 `chat.RequireApprovalOf` 按名称要求[审批](#approval)；批准后执行这个对话当前的 agent 上的同名工具。
- 它和[使用应用自己的消息表](#own-history)、[在 worker 中运行](#running-on-workers)可以一起使用，选择在运行所在的进程中进行。

只有模型不同、指令和工具都一样时，用上一节的 `llmagent.WithModelSelector` 就够了。

<a id="conversation-api"></a>
## 对话接口

助手的接口使用 [Vercel AI SDK](https://ai-sdk.dev) 的 [UI 消息流协议](https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol)，前端直接使用它的 `useChat`（React、Vue、Svelte 都有），流式显示、工具调用、审批和断线续传都不需要自己实现。

每个对话用一个 ID 标识，由 `useChat` 生成。对话属于当前登录的编辑，`Runs` 的方法都以主体 ID 和对话 ID 定位它，一个编辑无法读取其他人的对话。

服务端提供五个接口：

```go
type ChatInput struct {
    Body chat.UIRequest
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
    Body []chat.UIMessage
}

// POST /assistant/chat：发送消息或审批结果，响应是这次回答的消息流。
func (h *Handlers) Chat(ctx context.Context, in *ChatInput) (*huma.StreamResponse, error) {
    sub, _ := identity.FromContext(ctx)
    run, err := h.runs.Submit(ctx, sub.Ref().String(), in.Body)
    if err != nil {
        return nil, err
    }
    return chat.UIStream(run), nil
}

// GET /assistant/chat/{id}/stream：断线后接上进行中的回答。
func (h *Handlers) Resume(ctx context.Context, in *ConversationInput) (*huma.StreamResponse, error) {
    sub, _ := identity.FromContext(ctx)
    run, err := h.runs.Get(ctx, sub.Ref().String(), in.ID)
    if err != nil {
        return nil, err
    }
    return chat.UIResume(run), nil
}

// POST /assistant/chat/{id}/steer：回答进行中插话。
func (h *Handlers) Steer(ctx context.Context, in *SteerInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    return nil, h.runs.Steer(ctx, sub.Ref().String(), in.ID, model.NewUserMessage(in.Body.Text))
}

// POST /assistant/chat/{id}/stop：停止进行中的回答。
func (h *Handlers) Stop(ctx context.Context, in *ConversationInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    run, err := h.runs.Get(ctx, sub.Ref().String(), in.ID)
    if err != nil || run == nil {
        return nil, err // 没有进行中的回答时什么也不做
    }
    run.Cancel()
    return nil, nil
}

// GET /assistant/chat/{id}/messages：打开页面时加载对话历史。
func (h *Handlers) History(ctx context.Context, in *ConversationInput) (*HistoryOutput, error) {
    sub, _ := identity.FromContext(ctx)
    messages, err := h.runs.UIMessages(ctx, sub.Ref().String(), in.ID)
    return &HistoryOutput{messages}, err
}
```

`Runs` 的 `userID` 传 `sub.Ref().String()`，例如 `user:42`：只传 `sub.ID` 时，不同账号体系中 ID 相同的两个人会共用对话。

两个流式接口用 `rest.RegisterSSE` 注册，`chat.UIEvents` 描述消息流，用于生成 OpenAPI 文档：

```go
rest.RegisterSSE(api, op, chat.UIEvents, h.Chat)
```

前端按这些接口配置 `useChat`：

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

`Runs.Submit` 在后台开始一次运行后立即返回，运行不会因为请求结束而停止。它也不受 [`httpx.Timeout`](middleware.md#request-timeout) 的影响，助手的路由和其他 API 装在同一组中间件下即可。对话正在回答时再次提交，`Submit` 返回 `chat.ErrBusy`（`409`）。

<a id="stopping"></a>
### 停止回答

用户点击「停止」时，前端调用停止接口，不要只调用 `useChat` 的 `stop()`：

```tsx
const { status } = useChat({ /* 同上 */ });

async function stopAnswer() {
  await fetch(`/api/v1/assistant/chat/${conversationId}/stop`, { method: 'POST', headers });
}

{(status === 'submitted' || status === 'streaming') && <button onClick={stopAnswer}>停止</button>}
```

`stop()` 只断开浏览器的连接。回答在服务端后台运行，不随连接断开而停止：模型继续生成、继续调用工具、继续消耗 token，刷新页面后回答又接着出现。停止接口在服务端取消这次运行，消息流以 `abort` 结束，`useChat` 随之回到空闲状态，不需要再调用 `stop()`。

停止之后：

- 已经生成的内容保留在对话中，用户可以接着发消息；
- 模型的请求和正在执行的工具收到取消的 `ctx`，工具应当把它传给数据库查询和外部调用，才能尽快停下；已经完成的工具调用（例如已经发出的邮件）不会撤销；
- 部署了[多个实例](#multiple-instances)时，停止请求落在哪个实例上都可以，`Cancel` 转发给运行所在的实例，立即生效；
- 回答已经结束时调用停止接口什么也不做，前端不需要判断竞态。

<a id="steering"></a>
### 中途插话

回答进行中，`useChat` 的 `sendMessage` 会等这次回答结束后才发送。要让模型在回答过程中就看到新消息，前端改为调用插话接口：

```ts
await fetch(`/api/v1/assistant/chat/${conversationId}/steer`, { method: 'POST', body: JSON.stringify({ text }), headers })
```

- 模型正在调用工具时，消息在这批工具调用结束后、模型下一次思考之前进入对话。模型会同时看到工具的结果和这条新消息。
- 模型已经在给出最终回答时，它先答完，然后接着回答这条消息。

两种情况下，消息都不会打断模型，也不会丢失。消息进入对话时，回答中出现一个 `steering-message` 片段，前端把它显示在对话中。对话没有在回答时，`Runs.Steer` 返回 `chat.ErrFinished`，前端改用 `sendMessage`。

<a id="reconnecting"></a>
### 断线重连

`useChat` 设置了 `resume: true` 时，页面打开和网络恢复后会请求 `GET {api}/{id}/stream`。回答还在进行时，`chat.UIResume` 从头重放它，`useChat` 用重放的内容重建这条回答；没有进行中的回答时返回 `204`。

已经完成的内容由 `Runs.UIMessages` 从对话存储中读出，作为 `useChat` 的初始消息。

<a id="appending-messages"></a>
### 加入其他消息

对话中有时需要加入不是模型生成的消息：人写的回复、系统通知（「订单已发货」）、从邮件等其他渠道同步来的消息。`Runs.Append` 把它写进对话，模型在之后的回答中能看到：

```go
err := runs.Append(ctx, userID, conversationID, "staff:42", model.NewAssistantMessage("已为你补发，请留意短信"))
```

第三个参数是作者，由应用决定怎样命名。trpc-agent-go 把作者不是当前 agent 的消息作为用户消息交给模型；创建 agent 时加上 `llmagent.WithAddContextPrefix(true)`，消息前会带上 `[staff:42] said:`，模型才知道这句话是谁说的。

`UIMessages` 把这样的消息作为单独的一条返回，`metadata.author` 是作者，不会并入前后的回答，前端据此用不同的样式显示：

```tsx
{message.metadata?.author ? <StaffBubble message={message} /> : <Bubble message={message} />}
```

几条规则：

- 对话正在回答时返回 `chat.ErrBusy`，回答过程中要让模型看到用户的新消息用[插话](#steering)；有等待[审批](#approval)的工具调用时返回 `chat.ErrAwaitingApproval`。部署了多个实例时，这两种判断都对整个集群生效。
- trpc-agent-go 丢弃对话中第一条用户消息之前的内容，所以对话里还没有用户消息时，只能加入 `user` 角色的消息，否则返回错误。
- `Append` 只写入对话，不会推送给已经打开的页面。需要实时显示时，由应用通过[广播](broadcasting.md)通知页面，页面重新读取 `GET /assistant/chat/{id}/messages`。

<a id="approval"></a>
## 审批

`chat.RequireApproval` 列出的工具，模型调用时不会执行。运行在其他工具执行完后停下，消息流为每个调用发出 `tool-approval-request`，`useChat` 中这个工具片段的状态变为 `approval-requested`，前端显示确认按钮：

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

对话等待审批期间不能发送新消息，`Submit` 返回 `chat.ErrAwaitingApproval`（`409`），因为模型在得到调用结果之前不能回答新问题。等待审批前已经插入、还没有进入对话的消息，在审批之后的运行中送达。

<a id="own-history"></a>
## 使用应用自己的消息表

上面的对话保存在 `agent_` 开头的会话表中，适合「一个用户和一个助手」的对话。群聊、客服会话、从外部渠道接入的对话，消息往往已经保存在产品自己的消息表里：有多个参与者、消息序号、已读状态、编辑和删除。这时让 `Runs` 直接使用应用的表，不再保存第二份：

```go
runs := chat.NewRuns("luway", agent, nil, chat.UseHistory(chatHistory{db}))
```

`sessions` 传 `nil`。每一轮请求模型之前，`Runs` 用 `HistoryStore.Load` 读出上下文；运行中产生的完整消息（模型的回答、工具调用和结果）逐条交给 `HistoryStore.Save`。流式事件、停止、插话、审批、多实例协作和平滑停止的用法都不变，一对一助手同样可以使用：`Start` 和 `Submit` 发来的用户消息也交给 `Save` 保存。

`UIMessages` 和 `Append` 这时不可用（返回 `chat.ErrNeedsSessions`）：页面显示应用自己的消息，其他消息由应用直接写入表中。

<a id="history-store"></a>
### 实现 HistoryStore

```go
type HistoryStore interface {
    Load(ctx context.Context, c chat.Conversation) (chat.History, error)
    Save(ctx context.Context, c chat.Conversation, e chat.Entry) error
}
```

`chat.Conversation` 是 `Runs` 方法传入的 `userID` 和对话 ID。`Load` 返回交给模型的消息和一个游标，游标由应用定义，通常是读到的最大消息序号；这一轮产生的消息交给 `Save` 时，`Entry.After` 就是这个游标。

`Load` 要按**逻辑顺序**返回消息：`Save` 保存的消息排在它的 `After` 之后，而不是按写入时间。原因是模型回答期间，其他人可能还在发言：

| 写入顺序 | 逻辑顺序 |
| --- | --- |
| 张三：明天几点开会 | 张三：明天几点开会 |
| 李四：在哪开（回答期间发出） | AI：上午十点（基于张三之前的上下文） |
| AI：上午十点 | 李四：在哪开 |

按逻辑顺序，最后一条是李四的问题，下一轮接着回应它；按写入顺序，最后一条是 AI 的回答，李四的问题会被漏掉。页面仍然按写入顺序显示，逻辑顺序只用于交给模型。消息表多存一列 `after_seq` 即可：

```go
func (h chatHistory) Load(ctx context.Context, c chat.Conversation) (chat.History, error) {
    var rows []ChatMessage
    err := h.db.NewSelect().Model(&rows).Where("conversation_id = ?", c.ID).
        OrderExpr("COALESCE(after_seq, seq), after_seq IS NOT NULL, seq").Scan(ctx)
    if err != nil {
        return chat.History{}, err
    }
    var out chat.History
    var last int64
    for _, r := range rows {
        out.Messages = append(out.Messages, r.ModelMessage()) // 群成员的发言转为 user 消息，内容前加上名字
        last = max(last, r.Seq)
    }
    out.Cursor = strconv.FormatInt(last, 10)
    return out, nil
}

func (h chatHistory) Save(ctx context.Context, c chat.Conversation, e chat.Entry) error {
    row := NewChatMessage(c.ID, e.Message, e.RunID)
    if e.After != "" {
        after, _ := strconv.ParseInt(e.After, 10, 64)
        row.AfterSeq = &after
    }
    return h.insert(ctx, row) // 在事务中锁住会话，分配下一个 seq
}
```

几点约定：

- 返回哪些消息、怎样标明发言人、是否包括已编辑或删除的内容，由应用决定。
- 模型产生的带工具调用的 assistant 消息和工具结果要原样保存、原样返回：等待审批的调用由它们算出，审批之后在任何进程中都能继续。
- `Load` 的最后一条是模型的回答时，这一轮不请求模型。
- 多人共享的对话没有单一的用户，`userID` 由应用约定，例如传空字符串或租户 ID；谁能触发、谁能看，由应用在调用之前检查权限。

<a id="group-chat"></a>
### 群聊中的 AI

群成员的发言由应用照常写入消息表。需要 AI 回应时（例如它被 @ 了），写入之后调用 `Notify`：

```go
if mentionsAssistant(msg) {
    run, err := a.runs.Notify(ctx, "", conversationID)
    // ...
}
```

- AI 没有在回答时，`Notify` 开始一次运行，上下文由 `Load` 读出。
- AI 正在回答时（在任何一个实例上），`Notify` 记下通知，这次回答结束后再运行一轮，期间写入的消息都在新一轮的上下文中。回答期间无论来多少条消息、通知多少次，都只多运行一轮，不会返回 `ErrBusy`。
- 有等待审批的工具调用时返回 `chat.ErrAwaitingApproval`。

`Notify` 返回处理通知的运行。正在生成的回答用 `chat.UIStream(run)` 推给前端，群里的每个成员都可以用 `runs.Get(ctx, "", conversationID)` 接上它；回答的每条完整消息保存之后，由应用像普通消息一样通过[广播](broadcasting.md)推送。

<a id="media"></a>
## 图片、语音和向量

`ai` 包定义了五种能力，每种是一个只有一个方法的接口：

| 接口 | 方法 | 用途 |
| --- | --- | --- |
| `ai.Imager` | `Image(ctx, ai.ImageRequest) (ai.Media, error)` | 按描述生成图片，给出参考图时编辑或改绘 |
| `ai.Speaker` | `Speak(ctx, ai.SpeechRequest) (ai.Media, error)` | 把文字合成为语音 |
| `ai.Transcriber` | `Transcribe(ctx, ai.TranscriptionRequest) (string, error)` | 把语音识别为文字 |
| `ai.Embedder` | `Embed(ctx, texts []string) ([][]float32, error)` | 把文本转换为向量 |
| `ai.Reranker` | `Rerank(ctx, query string, docs []string, limit int) ([]ai.Ranked, error)` | 按与查询的相关度给文档排序 |

`ai.Media` 是图片或音频的内容和 MIME 类型。

`ai/openai` 通过 OpenAI 兼容接口实现这五种能力。`openai.New` 和 `chat.NewModel` 一样接收 `ai.ModelConfig`，不访问网络，可以在每次使用时按当时的配置创建。OpenAI、火山方舟、硅基流动、通义千问的兼容模式等都提供这类接口，但每家支持的能力不同，以厂商文档为准。

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

计算向量时一次传入多段文本。长文档先转换并切分为分段，见[文档转换与切分](documents.md)；保存和检索向量见[向量检索](vector.md)。有的厂商限制每批的数量，用 `openai.WithBatchSize` 让它分批请求；支持缩减维度的模型用 `openai.WithDimensions` 指定维度：

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

- **停机**：清理资源时，`runs.Shutdown` 不再开始新的回答（返回 `chat.ErrShuttingDown`，即 `503`），等进行中的回答说完，超过时限的才取消；取消的回答已经生成的部分保留在对话中，用户可以继续对话。等待期间 HTTP 服务已经停止，浏览器的消息流会断开：部署了[多个实例](#multiple-instances)时，浏览器重连到其他实例继续接收这次回答；单实例时，回答说完后保存在对话中，页面重新加载时可以看到。`Shutdown` 用的时间要计入部署平台的停止宽限期，见[平滑停止](deployment.md#graceful-shutdown)。
- **后台任务**：不需要实时显示的 AI 工作（例如生成摘要、给文章打标签）交给[队列](queue.md)执行，在处理函数中直接调用 trpc-agent-go 的 `runner.Run`。队列任务[至少执行一次](queue.md#introduction)，这些工具需要能安全地重复执行。

<a id="multiple-instances"></a>
### 多个实例

一次运行在启动它的实例上执行。部署多个实例时，用 `chat.Cluster` 把各实例的 `Runs` 连起来，同一个对话的请求就可以落在任何一个实例上：

```go
runs := chat.NewRuns("blog", agent, sessions,
    chat.RequireApproval(publish),
    chat.Cluster(hub, locker),
)
```

`hub` 是[广播](broadcasting.md#creating-the-hub)的 Hub，`locker` 是[原子锁](locks.md)，都在 `open` 中创建，所有实例必须使用同一个 Redis。

- 对话正在运行时，运行所在的实例持有这个对话的锁。其他实例上的 `Submit`、`Start` 因此返回 `chat.ErrBusy`，不会同时运行两次。
- 运行的事件每 50 毫秒批量广播一次。其他实例上的 `Runs.Get` 先向运行所在的实例要一份目前为止的快照，再接收之后的事件，返回的 `Run` 用法与本地的相同；`Steer` 和 `Cancel` 转发给运行所在的实例，`Steer` 等待它的回执。
- 运行所在的实例意外退出时，锁在 30 秒内过期；镜像 15 秒收不到消息就以一个 `error` 事件结束，浏览器重新续传时得到最新的状态。

审批读写的是对话存储，本来就不依赖实例。

<a id="running-on-workers"></a>
### 在 worker 中运行

默认情况下，回答在接到请求的 HTTP 实例上运行。把它们交给 worker 进程，HTTP 实例的部署和扩缩容就不会打断回答，AI 的负载也不占用 HTTP 进程：

```go
runs := chat.NewRuns("blog", agent, sessions,
    chat.RequireApproval(publish),
    chat.Cluster(hub, locker),
    chat.RunOnWorkers(),
)
```

worker 进程在处理队列的同时调用 `runs.Serve`，`a.jobs` 是原来装配[队列 worker](queue.md#running-workers) 的 `Worker` 方法：

```go
func (a *App) Worker(d *command.Daemon) (func(context.Context) error, error) {
    jobs, err := a.jobs(d)
    if err != nil {
        return nil, err
    }
    return func(ctx context.Context) error {
        return command.Concurrently(ctx, jobs, a.runs.Serve)
    }, nil
}
```

HTTP 实例上的 `Submit`、`Start`、`Notify`、`Approve`、`Reject` 把请求转交给某一个 worker，在那里开始运行，返回的是这次运行的镜像，`UIStream`、插话、停止的用法都不变。多个 worker 同时 `Serve` 时，每个请求只由其中一个处理；需要审批的工具也在 worker 上执行。用 `start` 在一个进程中运行全部部分时，同一个进程里的 `Serve` 接手请求，不需要改代码。

- **没有 worker：** 请求经由广播转交、不排队。没有 worker 在 `Serve`，或者它们都在停止时，返回 `chat.ErrNoWorker`（`503`），由前端稍后重试。
- **停止与崩溃：** worker 收到停止信号后不再接手新的请求，进行中的回答由 `runs.Shutdown` 等它说完。worker 崩溃时进行中的回答随之结束，不会在其他实例上接着运行，这与在 HTTP 进程中运行时相同。
- **运行选项：** `agent.RunOption` 不能跨进程传递，传入时返回 `chat.ErrRunOptions`。需要的设置放在 worker 创建的 agent 上。

<a id="testing"></a>
## 测试

`chattest.NewModel` 是一个按脚本回复的模型，不访问真实的模型服务。用它代替 `chat.NewModel`，就能测试工具调用、插话和审批的完整流程：

```go
m := chattest.NewModel(
    chattest.Calls(chattest.Call{Name: "search_articles", Args: assistant.SearchArgs{Query: "Go"}}),
    chattest.Text("找到 3 篇相关文章"),
)
```

模型依次给出这两个回复：先调用 `search_articles`，拿到结果后回答。`m.Requests()` 返回模型收到的请求，可以检查发给模型的历史和工具结果。会话存储使用 trpc-agent-go 的 `inmemory.NewSessionService()`。

图片、语音、向量和决策的替身在 `ai/aitest` 中：

```go
img := aitest.NewImager()            // 每次返回 aitest.PNG
emb := aitest.NewEmbedder(1024)      // 相同的文本总是得到相同的向量
sp := aitest.NewSpeaker()            // 把朗读的文字当作音频内容返回
tr := aitest.NewTranscriber("你好")  // 依次返回预设的文字
rr := aitest.NewReranker()           // 按查询中的字符在文档中出现的比例排序
dc := aitest.NewDecider()            // 是非题 0.5，选择题选第一项，打分题最低一级
```

传入预设结果时按顺序返回，用完后再收到请求就返回错误；`Requests()` 返回收到的请求，`FailWith(err)` 让之后的请求失败，用于测试出错的处理。
