// Package push 向 App 发送移动推送通知（iOS、Android、HarmonyOS）。
//
// 设备由服务端登记：App 登录后把推送 SDK 给出的设备标识交给应用的接口，接口调用 Register，
// 把设备记在当前主体名下，同时记下所用的凭据。设备归谁由服务端决定，客户端不能把自己登记成别人；
// 退出登录、撤销凭据时用 Unregister 系列函数删除设备，这台设备就不再收到推送。
//
// 发送分两步，与 webhook 相同。Send 在业务事务中发布推送任务，事务提交后才会推送；
// worker 用 Deliverer 处理任务：查出收件人的设备，按平台分批交给 Provider：
//
//	err := push.Send(ctx, tx, push.Message{Title: "文章已发布", Body: title, Data: map[string]string{"article_id": id}}, author)
//
//	provider, err := aliyun.New(cfg.Push, nil)
//	push.NewDeliverer(db, provider, push.Options{}).Handle(w)
//
// Provider 对接具体的推送服务。push/aliyun 对接阿里云移动推送，国内经各手机厂商的通道送达，
// 海外经 FCM 和 APNs 送达；push/pushtest 记录推送，用于测试。
package push
