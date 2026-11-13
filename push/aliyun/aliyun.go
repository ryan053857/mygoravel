// Package aliyun 是对接阿里云移动推送（EMAS Push）的 push.Provider，调用 PushV2 接口按设备推送通知。
//
// 国内的 Android 设备经华为、荣耀、小米、OPPO、vivo、魅族的厂商通道送达，海外经 FCM；iOS 经 APNs，
// HarmonyOS 经鸿蒙的推送服务。各通道的密钥在阿里云控制台配置，App 集成阿里云的推送 SDK 和厂商通道的依赖，
// 把 SDK 给出的 deviceId 交给服务端，由 push.Register 登记。
//
//	provider, err := aliyun.New(cfg.Push, nil)
//	push.NewDeliverer(db, provider, push.Options{}).Handle(w)
//
// 阿里云中每个平台是一个应用，各有自己的 AppKey，同一条消息按平台分别调用。
package aliyun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/go-tenon/tenon/internal/aliyunapi"
	"github.com/go-tenon/tenon/outbound"
	"github.com/go-tenon/tenon/push"
	"github.com/go-tenon/tenon/queue"
)

// DefaultEndpoint 是 Config.Endpoint 为空时调用的接口域名，国内和海外的应用都使用它。
const DefaultEndpoint = "cloudpush.aliyuncs.com"

// Timeout 是 New 创建的默认客户端的超时。
const Timeout = 10 * time.Second

// version 是移动推送 OpenAPI 的版本。
const version = "2016-08-01"

// Config 是阿里云移动推送的配置段。
//
//	push:
//	  access_key_id: ${ALIYUN_ACCESS_KEY_ID}
//	  access_key_secret: ${ALIYUN_ACCESS_KEY_SECRET}
//	  app_keys: {android: 335000001, ios: 335000002}
//	  android:
//	    activity: com.example.blog.PushPopupActivity
//	    channel: general
//	  categories:
//	    default: {huawei: WORK, honor: 1, vivo: TODO, oppo: TODO, xiaomi: "135001"}
//	    promo:   {huawei: MARKETING, honor: 0, vivo: MARKETING, oppo: MARKETING, channel: promo}
type Config struct {
	// AccessKeyID 和 AccessKeySecret 是 RAM 用户的 AccessKey，需要 mpush:PushV2 权限。
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	// Endpoint 是接口的域名，为空时是 DefaultEndpoint。
	Endpoint string `yaml:"endpoint"`
	// AppKeys 是各平台的应用在阿里云中的 AppKey，没有的平台填 0；给没有 AppKey 的平台推送时任务失败、不再重试。
	AppKeys AppKeys `yaml:"app_keys"`
	// APNs 是 iOS 推送的环境：PRODUCT（默认）用于 App Store、TestFlight 和企业分发的 App，DEV 用于 Xcode 直接安装的 App。
	APNs string `yaml:"apns"`
	// Android 是 Android 通知的公共设置。
	Android Android `yaml:"android"`
	// Categories 把 push.Message.Category 换成各厂商的消息分类参数。消息没有指定分类时使用 default；
	// 指定了这里没有的分类时任务失败、不再重试。没有配置 default 时，不指定分类的消息不带厂商分类参数，
	// 厂商通常把它当作资讯营销消息，每台设备每天能收到的条数有限。
	Categories map[string]Category `yaml:"categories"`
}

// AppKeys 是各平台的应用的 AppKey。
type AppKeys struct {
	Android   int64 `yaml:"android"`
	IOS       int64 `yaml:"ios"`
	HarmonyOS int64 `yaml:"harmony"`
}

// Android 是 Android 通知的公共设置。
type Android struct {
	// Activity 是厂商通道的辅助弹窗 Activity 的完整类名（VendorChannelActivity），使用厂商通道时必填，
	// 用户点击经厂商通道送达的通知时打开它。
	Activity string `yaml:"activity"`
	// Channel 是 Android 8 及以上的 NotificationChannel ID，App 需要先创建这个渠道；分类可以覆盖它。
	Channel string `yaml:"channel"`
}

// Category 是一个消息分类在各厂商通道中的参数，取值见各厂商的消息分类规范，大多需要先在厂商后台申请。
// 没有填写的厂商不带分类参数。
type Category struct {
	// Channel 覆盖 Android.Channel，例如营销消息使用单独的通知渠道，用户可以只关闭它。
	Channel string `yaml:"channel"`
	// Huawei 是华为的 category，例如 IM、WORK、EXPRESS；营销消息为 MARKETING。
	Huawei string `yaml:"huawei"`
	// Honor 是荣耀的 importance：1 为服务与通讯，0 为资讯营销。
	Honor *int `yaml:"honor"`
	// Vivo 是 vivo 的 category，例如 IM、ACCOUNT、TODO；营销消息为 MARKETING。
	Vivo string `yaml:"vivo"`
	// Oppo 是 OPPO 的 category，例如 IM、ACCOUNT、ORDER；营销消息为 MARKETING。
	Oppo string `yaml:"oppo"`
	// Xiaomi 是小米的 channel ID，在小米推送后台申请，每个应用最多 8 个。
	Xiaomi string `yaml:"xiaomi"`
	// HarmonyOS 是鸿蒙的 category，例如 IM、WORK、EXPRESS；营销消息为 MARKETING。
	HarmonyOS string `yaml:"harmony"`
}

// Validate 检查必填项和取值范围。
func (c Config) Validate() error {
	var errs []error
	if c.AccessKeyID == "" || c.AccessKeySecret == "" {
		errs = append(errs, errors.New("access_key_id and access_key_secret are required"))
	}
	if c.AppKeys == (AppKeys{}) {
		errs = append(errs, errors.New("app_keys must have at least one platform"))
	}
	if c.Endpoint != "" && (strings.ContainsAny(c.Endpoint, "/:") || c.Endpoint != strings.TrimSpace(c.Endpoint)) {
		errs = append(errs, errors.Newf("endpoint %q must be a host name, such as %s", c.Endpoint, DefaultEndpoint))
	}
	if c.APNs != "" && c.APNs != "PRODUCT" && c.APNs != "DEV" {
		errs = append(errs, errors.Newf("apns %q must be PRODUCT or DEV", c.APNs))
	}
	for name, cat := range c.Categories {
		if cat.Honor != nil && *cat.Honor != 0 && *cat.Honor != 1 {
			errs = append(errs, errors.Newf("categories.%s.honor must be 0 or 1", name))
		}
	}
	return errors.Join(errs...)
}

// Provider 调用阿里云移动推送的 PushV2 接口，可以并发使用。
type Provider struct {
	cfg Config
	api *aliyunapi.Client
}

var _ push.Provider = (*Provider)(nil)

// New 创建 Provider，不访问网络。client 为 nil 时使用 outbound.New 创建的客户端，超时为 Timeout；
// 测试时传入 outboundtest 的客户端。
func New(cfg Config, client *http.Client) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "aliyun push")
	}
	host := cfg.Endpoint
	if host == "" {
		host = DefaultEndpoint
	}
	if client == nil {
		client = outbound.New(outbound.Options{Timeout: Timeout})
	}
	api := &aliyunapi.Client{Host: host, Version: version, AccessKeyID: cfg.AccessKeyID, AccessKeySecret: cfg.AccessKeySecret, HTTP: client}
	return &Provider{cfg: cfg, api: api}, nil
}

// Push 把 b 作为一个 PushV2 任务推送给 b.Tokens 中的设备（阿里云的 deviceId）。
//
// b.Key 作为 IdempotentToken：15 分钟内用相同的 Key 再次调用只推送一次。b.Job 作为 JobKey 写入回执日志。
// 配置或参数错误（4xx）用 queue.Permanent 包装，限流、服务端错误和网络错误可以重试。
func (p *Provider) Push(ctx context.Context, b push.Batch) error {
	appKey := p.appKey(b.Platform)
	if appKey == 0 {
		return queue.Permanent(errors.Newf("aliyun push: no app key for platform %q", b.Platform))
	}
	task, err := p.task(b)
	if err != nil {
		return queue.Permanent(err)
	}
	taskJSON, err := json.Marshal(task)
	if err != nil {
		return queue.Permanent(errors.Wrap(err, "aliyun push: encode task"))
	}
	params := url.Values{
		"AppKey":   {strconv.FormatInt(appKey, 10)},
		"PushTask": {string(taskJSON)},
	}
	if b.Key != "" {
		params.Set("IdempotentToken", b.Key)
	}
	return p.call(ctx, "PushV2", params)
}

func (p *Provider) appKey(platform push.Platform) int64 {
	switch platform {
	case push.Android:
		return p.cfg.AppKeys.Android
	case push.IOS:
		return p.cfg.AppKeys.IOS
	case push.HarmonyOS:
		return p.cfg.AppKeys.HarmonyOS
	}
	return 0
}

// task 把 b 转换为 PushV2 的 PushTask。
func (p *Provider) task(b push.Batch) (*pushTask, error) {
	msg := b.Message
	name := msg.Category
	if name == "" {
		name = "default"
	}
	cat, ok := p.cfg.Categories[name]
	if !ok && msg.Category != "" {
		return nil, errors.Newf("aliyun push: category %q is not configured", msg.Category)
	}
	var ext string
	if len(msg.Data) > 0 {
		data, err := json.Marshal(msg.Data)
		if err != nil {
			return nil, errors.Wrap(err, "aliyun push: encode data")
		}
		ext = string(data)
	}
	t := &pushTask{
		Target:       target{Type: "DEVICE", Value: strings.Join(b.Tokens, ",")},
		Notification: notification{Title: msg.Title, Body: msg.Body},
		Options:      options{Trim: true},
	}
	if b.Job != uuid.Nil {
		t.Options.JobKey = b.Job.String()
	}
	if !b.Expires.IsZero() {
		t.Options.ExpireTime = b.Expires.UTC().Format("2006-01-02T15:04:05Z")
	}
	switch b.Platform {
	case push.Android:
		a := &androidNotification{
			VendorChannelActivity: p.cfg.Android.Activity,
			ChannelID:             p.cfg.Android.Channel,
			ExtParameters:         ext,
		}
		if cat.Channel != "" {
			a.ChannelID = cat.Channel
		}
		if msg.Badge != nil && *msg.Badge > 0 {
			a.BadgeSetNum = min(*msg.Badge, 99)
		}
		var o androidOptions
		if cat.Huawei != "" {
			o.Huawei = &vendorCategory{Category: cat.Huawei}
		}
		if cat.Honor != nil {
			o.Honor = &honorOptions{Importance: *cat.Honor}
		}
		if cat.Vivo != "" {
			o.Vivo = &vendorCategory{Category: cat.Vivo}
		}
		if cat.Oppo != "" {
			o.Oppo = &vendorCategory{Category: cat.Oppo}
		}
		if cat.Xiaomi != "" {
			o.Xiaomi = &xiaomiOptions{Channel: cat.Xiaomi}
		}
		if o != (androidOptions{}) {
			a.Options = &o
		}
		t.Notification.Android = a
	case push.IOS:
		env := p.cfg.APNs
		if env == "" {
			env = "PRODUCT"
		}
		t.Notification.IOS = &iosNotification{ApnsEnv: env, Music: "default", Badge: msg.Badge, ExtParameters: ext}
	case push.HarmonyOS:
		h := &harmonyNotification{Category: cat.HarmonyOS, ExtParameters: ext}
		if msg.Badge != nil && *msg.Badge >= 0 {
			h.BadgeSetNum = new(min(*msg.Badge, 99))
		}
		t.Notification.HarmonyOS = h
	default:
		return nil, errors.Newf("aliyun push: unknown platform %q", b.Platform)
	}
	return t, nil
}

// call 调用接口 action，把失败分为可以重试的和不能重试的（queue.Permanent）。
func (p *Provider) call(ctx context.Context, action string, params url.Values) error {
	resp, err := p.api.Call(ctx, action, params)
	if err != nil {
		return errors.Wrap(err, "aliyun push")
	}
	if resp.Status == http.StatusOK {
		return nil
	}
	err = errors.Wrap(resp.Error(action), "aliyun push")
	if retryable(resp.Status, resp.Code) {
		return err
	}
	return queue.Permanent(err)
}

// retryable 报告失败的调用能否重试：限流、服务端错误和超时可以，其他 4xx 是配置或参数错误，重试也不会成功。
func retryable(status int, code string) bool {
	if status >= 500 || status == http.StatusTooManyRequests {
		return true
	}
	return strings.HasPrefix(code, "Throttling") || strings.HasPrefix(code, "ServiceUnavailable") ||
		code == "InternalError" || code == "Timeout"
}
