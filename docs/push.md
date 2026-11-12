# 移动推送

- [简介](#introduction)
- [安装](#installation)
    - [阿里云控制台](#aliyun-console)
    - [App 端](#app-side)
- [登记设备](#registering-devices)
    - [退出登录](#unregistering)
- [发送推送](#sending)
    - [消息](#messages)
    - [消息分类](#categories)
- [运行推送](#delivering)
    - [重试与去重](#retries)
- [海外用户](#overseas)
- [测试](#testing)
- [清理](#cleanup)

<a id="introduction"></a>
## 简介

App 在后台或者没有打开时，只能靠系统的推送通道把消息送到用户面前。iOS 走 APNs，海外的 Android 走 FCM；国内的 Android 没有统一的通道，要分别接入华为、荣耀、小米、OPPO、vivo、魅族的厂商通道，再按各家的规则给消息分类。`push` 包把这件事交给[阿里云移动推送](https://help.aliyun.com/zh/document_detail/434629.html)：各通道的接入和密钥由阿里云管理，框架负责“推给谁”和“怎么可靠地推”。

- **设备由服务端登记**：App 登录后把推送 SDK 给出的设备标识交给你的接口，接口把设备记在当前用户名下，同时记下这次登录的凭据。客户端不能把自己登记成别人，退出登录时连同凭据一起删除，这台设备就不再收到推送。
- **推送随事务提交**：`push.Send` 在业务事务中发布推送任务，事务提交后才推送，回滚时一起取消，与[站内通知](notifications.md)、[webhook](webhooks.md) 的写法相同。一次通知发往哪些渠道，仍由 action 显式写出来。

本章的例子沿用[读者 App](account-examples.md#reader-api)：编辑发布文章后，推送给订阅了这个栏目的读者。

<a id="installation"></a>
## 安装

推送任务使用[队列](queue.md)，把两者的迁移都加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`：

```go
migratecmd.Publish(
    migrate.Source{Name: "queue", FS: queue.Migrations()},
    migrate.Source{Name: "push", FS: push.Migrations()},
)
```

<a id="aliyun-console"></a>
### 阿里云控制台

1. 在 EMAS 控制台为每个平台各创建一个应用（Android、iOS、HarmonyOS），记下各自的 **AppKey**。同一条消息按平台分别调用，所以框架按平台分批。
2. 在各应用的推送配置中填写通道的密钥：华为、荣耀、小米、OPPO、vivo、魅族的厂商通道，iOS 的 APNs 证书，海外 Android 的 FCM 服务账号 JSON。
3. 创建一个 RAM 用户，授予 `mpush:PushV2` 权限，把它的 AccessKey 写进配置。

```yaml
# config/app.yaml
push:
  access_key_id: ${ALIYUN_ACCESS_KEY_ID}
  access_key_secret: ${ALIYUN_ACCESS_KEY_SECRET}
  app_keys: {android: 335000001, ios: 335000002}
  android:
    activity: com.example.blog.PushPopupActivity
    channel: general
  categories:
    default: {huawei: SUBSCRIPTION, honor: 1, vivo: SUBSCRIPTION, oppo: SUBSCRIPTION, xiaomi: "135001", harmony: SUBSCRIPTION}
```

`apns` 默认是 `PRODUCT`，用于 App Store、TestFlight 和企业分发的包；开发环境用 Xcode 直接安装的包时设为 `DEV`。`categories` 见[消息分类](#categories)。

<a id="app-side"></a>
### App 端

App 集成阿里云的推送 SDK 和各厂商通道的依赖，按[阿里云的文档](https://help.aliyun.com/zh/document_detail/434660.html)完成初始化。和服务端有关的只有三件事：

- **创建通知渠道**：Android 8 及以上要先创建 NotificationChannel，ID 与配置中的 `android.channel` 一致。
- **辅助弹窗 Activity**：经厂商通道送达的通知被点击时，系统打开 `android.activity` 指定的 Activity，在其中读出 `Data` 并跳转。
- **不要调用 SDK 的 `bindAccount`**：账号由服务端登记，App 只把 `deviceId` 交给服务端。

<a id="registering-devices"></a>
## 登记设备

App 登录成功后、以及每次启动时，把 SDK 的 `deviceId` 交给 `PUT /devices`。处理器调用 `push.Register`，设备登记在当前读者名下：

```go
type DeviceInput struct {
    Body struct {
        Platform push.Platform `json:"platform" enum:"android,ios,harmony"`
        Token    string        `json:"token" minLength:"1" maxLength:"512" doc:"推送 SDK 的 deviceId"`
    }
}

rest.Register(api, signedIn("reader-device", http.MethodPut, "/devices"), h.registerDevice)

func (h *handlers) registerDevice(ctx context.Context, in *DeviceInput) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    return nil, push.Register(ctx, h.db, sub, in.Body.Platform, in.Body.Token)
}
```

一台设备同时只属于一个用户：同一部手机换了账号登录，设备改为登记给新的读者，旧账号不会再收到推送。读者在多台设备上登录时，每台都会收到。

`Register` 同时记下主体的租户和 `sub.Credential`（这次登录的 token ID）。[多租户](tenancy.md)应用中，发给一个租户的推送只到达在这个租户中登记的设备。

<a id="unregistering"></a>
### 退出登录

撤销凭据时一起删除设备，三个函数与 [`apitoken` 的撤销方法](api-tokens.md#revoking-tokens)一一对应：

| 撤销 token | 删除设备 | 用途 |
| --- | --- | --- |
| `Revoke(ctx, sub, id)` | `push.Unregister(ctx, db, sub, id)` | 退出登录（`id` 为 `sub.Credential`）、在设备列表中移除某台设备 |
| `RevokeOthers(ctx, sub)` | `push.UnregisterOthers(ctx, db, sub)` | 退出其他设备 |
| `RevokeSubject(ctx, ref)` | `push.UnregisterSubject(ctx, db, ref)` | 封禁、注销账号 |

读者 App 的退出登录变成：

```go
func (h *handlers) logout(ctx context.Context, _ *struct{}) (*struct{}, error) {
    sub, _ := identity.FromContext(ctx)
    if _, err := push.Unregister(ctx, h.db, sub, sub.Credential); err != nil {
        return nil, err
    }
    return nil, h.acc.ReaderTokens.Revoke(ctx, sub, sub.Credential)
}
```

忘了调用时，设备在登录过期之后仍然登记着，会继续收到推送，直到[清理](#cleanup)或者被别的账号重新登记。

<a id="sending"></a>
## 发送推送

在[发布文章](actions.md#writing-actions)的事务中调用 `push.Send`，推送与文章状态一起提交：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    // ……读取、检查并更新文章，与原来相同
    readers, err := subscribers(ctx, tx, item.Section) // 应用自己的查询，返回 []identity.Ref
    if err != nil {
        return err
    }
    to := make([]notification.Recipient, len(readers))
    for i, r := range readers {
        to[i] = notification.Recipient{Ref: r}
    }
    return push.Send(ctx, tx, push.Message{
        Title: "新文章",
        Body:  item.Title,
        Data:  map[string]string{"article_id": item.ID.String()},
    }, to...)
})
```

收件人与[站内通知](notifications.md#recipients)的相同：某个租户中的一个主体，同一个变量可以同时交给 `notification.Send` 和 `push.Send`。`Send` 只发布任务，设备在任务执行时才查询；收件人没有登记设备时什么也不做。重复的收件人只推送一次，超过 1000 个时分成多个任务。

<a id="messages"></a>
### 消息

| 字段 | 说明 |
| --- | --- |
| `Title`、`Body` | 通知的标题和正文，必填。文案由应用按收件人的语言生成，见[本地化](localization.md) |
| `Data` | 随通知带给 App 的键值，用户点击后 App 据此打开对应的页面 |
| `Category` | [消息分类](#categories)，为空时是 `default` |
| `Badge` | App 图标上的数字，`nil` 表示不修改。iOS 和鸿蒙可以设为 0 清除；Android 只支持 1 到 99，并且只在华为、荣耀和 vivo 手机上生效 |
| `TTL` | 设备离线时通知保留多久，从 `Send` 开始计算；阿里云最长 72 小时。队列积压超过 TTL 的推送不再发出，适合验证码、限时活动这类过时就没有意义的消息 |

通知不带内容以外的敏感信息：锁屏界面上任何人都看得到。需要展示私密内容时，`Body` 写“你有一条新消息”，App 打开后再从接口读取。

<a id="categories"></a>
### 消息分类

国内厂商把通知分成两类：**服务与通讯**（订单、聊天、订阅更新）不限条数、正常提醒；**资讯营销**每台设备每天只能收到很少几条（华为一般应用每天 2 条），并且静默展示。不带分类参数的消息，厂商通常按资讯营销处理。

分类参数各家不同，大多需要先在厂商后台申请。`Message.Category` 是应用自己起的名字，配置中的 `categories` 把它换成各家的参数：

```yaml
categories:
  default: {huawei: SUBSCRIPTION, honor: 1, vivo: SUBSCRIPTION, oppo: SUBSCRIPTION, xiaomi: "135001", harmony: SUBSCRIPTION}
  promo:   {huawei: MARKETING, honor: 0, vivo: MARKETING, oppo: MARKETING, harmony: MARKETING, xiaomi: "135002", channel: promo}
```

| 键 | 厂商参数 |
| --- | --- |
| `huawei`、`vivo`、`oppo`、`harmony` | 各家的 `category`，取值见各厂商的消息分类规范，营销消息为 `MARKETING` |
| `honor` | 荣耀的 `importance`：1 为服务与通讯，0 为资讯营销 |
| `xiaomi` | 小米推送后台申请的 channel ID，每个应用最多 8 个 |
| `channel` | 覆盖 `android.channel`，例如营销消息用单独的通知渠道，用户可以只关闭它 |

推送营销消息时写 `Category: "promo"`。指定了配置中没有的分类时，推送任务失败、不再重试，在[失败任务](queue.md#failed-jobs)中可以看到原因。

<a id="delivering"></a>
## 运行推送

在 [worker](queue.md#processing-jobs) 中注册推送任务的处理器。`aliyun.New` 不访问网络：

```go
provider, err := aliyun.New(cfg.Push, nil)
if err != nil {
    return err
}
push.NewDeliverer(db, provider, push.Options{}).Handle(w)
```

处理器查出收件人的设备，按平台分批，每批最多 1000 台，调用阿里云的 [PushV2](https://help.aliyun.com/zh/document_detail/2986003.html) 接口。阿里云接受了请求就算成功，之后经厂商通道送达、用户点击这些结果，在控制台的“回执日志”中查看，每条记录的 `job_key` 是推送任务的 ID。

<a id="retries"></a>
### 重试与去重

- 限流、阿里云服务端错误和网络错误按队列的退避重试，默认最多 5 次，约 6 分钟。
- AccessKey、AppKey、参数错误等重试也不会成功的错误，直接转入失败任务。
- 一个任务中有一批失败时整个任务重试。已经成功的批次再次调用时带着相同的 `IdempotentToken`，阿里云在 15 分钟内只推送一次，用户不会收到重复的通知。

<a id="overseas"></a>
## 海外用户

海外用户使用同一个应用和同一套代码：iOS 本来就经 APNs 送达，Android 在控制台配置 FCM 通道、App 集成阿里云的 FCM 依赖后，有 Google 服务的手机经 FCM 送达。接口域名 `cloudpush.aliyuncs.com` 在海外同样可以访问。

没有 Google 服务、也不是国内厂商的 Android 手机，只能在 App 运行时经阿里云自有的长连接收到。

<a id="testing"></a>
## 测试

只检查发布了哪些推送时，不需要运行 worker，用 `queuetest.Pending` 读出还没处理的推送任务：

```go
jobs := queuetest.Pending(t, db, push.Deliver)
if len(jobs) != 1 || jobs[0].Message.Title != "新文章" {
    t.Fatalf("jobs %+v", jobs)
}
```

端到端测试时，用 `pushtest.Provider` 代替阿里云，它记录收到的每一批推送，`Err` 可以模拟失败：

```go
fake := &pushtest.Provider{}
w := queue.NewWorker(db, queuetest.Config(t), nil)
push.NewDeliverer(db, fake, push.Options{}).Handle(w)
queuetest.Run(t, w)
// ……发布文章，等待推送
tokens := fake.Tokens()
```

测试 `push/aliyun` 的配置时，把 [`outboundtest`](outbound.md#testing) 的客户端传给 `aliyun.New`。

<a id="cleanup"></a>
## 清理

App 每次启动都会重新登记，长期没有更新的设备通常已经卸载了 App。用 `push.Cleanup` 定期删除它们：

```go
sched.Add("push:cleanup", "@daily", schedule.Cleanup(push.Cleanup(a.db, 90*24*time.Hour), 1000))
```
