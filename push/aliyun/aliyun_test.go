package aliyun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/outbound/outboundtest"
	"github.com/ryan053857/mygoravel/push"
	"github.com/ryan053857/mygoravel/queue"
)

const api = "POST https://cloudpush.aliyuncs.com/"

func config() Config {
	one, zero := 1, 0
	return Config{
		AccessKeyID: "testid", AccessKeySecret: "testsecret",
		AppKeys: AppKeys{Android: 335000001, IOS: 335000002, HarmonyOS: 335000003},
		Android: Android{Activity: "com.example.blog.PushPopupActivity", Channel: "general"},
		Categories: map[string]Category{
			"default": {Huawei: "WORK", Honor: &one, Vivo: "TODO", Oppo: "TODO", Xiaomi: "135001", HarmonyOS: "WORK"},
			"promo":   {Huawei: "MARKETING", Honor: &zero, Vivo: "MARKETING", Oppo: "MARKETING", HarmonyOS: "MARKETING", Channel: "promo"},
		},
	}
}

func newProvider(t *testing.T, cfg Config) (*Provider, *outboundtest.Transport) {
	t.Helper()
	fake := outboundtest.New(t)
	p, err := New(cfg, fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	return p, fake
}

func batch(platform push.Platform) push.Batch {
	badge := 3
	return push.Batch{
		Key:      "3c9a2f6e-0b1d-4e7a-9f1c-2d5e8a7b6c40",
		Job:      uuid.MustParse("0193e7b1-2c4f-7d8e-9a1b-5f6c7d8e9f01"),
		Platform: platform,
		Tokens:   []string{"dev1", "dev2"},
		Message: push.Message{
			Title: "文章已发布", Body: "Hello & 你好", Data: map[string]string{"article_id": "42"}, Badge: &badge,
		},
	}
}

// sent 返回唯一的一次请求的参数和解析后的 PushTask。
func sent(t *testing.T, fake *outboundtest.Transport) (map[string]string, map[string]any) {
	t.Helper()
	reqs := fake.Sent(api)
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	form, err := reqs[0].Form()
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{}
	for k := range form {
		params[k] = form.Get(k)
	}
	var task map[string]any
	if err := json.Unmarshal([]byte(params["PushTask"]), &task); err != nil {
		t.Fatal(err)
	}
	return params, task
}

// at 按点分隔的路径取出嵌套的值。
func at(v any, path string) any {
	for _, key := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

func TestRequest(t *testing.T) {
	p, fake := newProvider(t, config())
	fake.On(api).Reply(outboundtest.JSON(200, map[string]string{"RequestId": "r1", "MessageId": "m1"}))
	if err := p.Push(context.Background(), batch(push.Android)); err != nil {
		t.Fatal(err)
	}
	req := fake.Sent(api)[0]
	h := req.Header
	sum := sha256.Sum256(req.Body)
	if h.Get("x-acs-action") != "PushV2" || h.Get("x-acs-version") != "2016-08-01" ||
		h.Get("x-acs-content-sha256") != hex.EncodeToString(sum[:]) ||
		h.Get("Content-Type") != "application/x-www-form-urlencoded" ||
		!strings.HasPrefix(h.Get("Authorization"), "ACS3-HMAC-SHA256 Credential=testid,SignedHeaders=content-type;host;") {
		t.Fatalf("headers %v", h)
	}
	if _, err := time.Parse("2006-01-02T15:04:05Z", h.Get("x-acs-date")); err != nil {
		t.Fatal(err)
	}
	params, _ := sent(t, fake)
	if params["AppKey"] != "335000001" || params["IdempotentToken"] != "3c9a2f6e-0b1d-4e7a-9f1c-2d5e8a7b6c40" {
		t.Fatalf("params %v", params)
	}
}

func TestAndroidTask(t *testing.T) {
	p, fake := newProvider(t, config())
	fake.On(api).Reply(outboundtest.JSON(200, map[string]string{"RequestId": "r1", "MessageId": "m1"}))
	b := batch(push.Android)
	b.Expires = time.Date(2026, 11, 14, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	if err := p.Push(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	_, task := sent(t, fake)
	for path, want := range map[string]any{
		"Target.Type":        "DEVICE",
		"Target.Value":       "dev1,dev2",
		"Notification.Title": "文章已发布",
		"Notification.Body":  "Hello & 你好",
		"Notification.Android.VendorChannelActivity":    "com.example.blog.PushPopupActivity",
		"Notification.Android.ChannelId":                "general",
		"Notification.Android.ExtParameters":            `{"article_id":"42"}`,
		"Notification.Android.BadgeSetNum":              float64(3),
		"Notification.Android.Options.Huawei.Category":  "WORK",
		"Notification.Android.Options.Honor.Importance": float64(1),
		"Notification.Android.Options.Vivo.Category":    "TODO",
		"Notification.Android.Options.Oppo.Category":    "TODO",
		"Notification.Android.Options.Xiaomi.Channel":   "135001",
		"Options.ExpireTime":                            "2026-11-14T00:00:00Z",
		"Options.JobKey":                                "0193e7b1-2c4f-7d8e-9a1b-5f6c7d8e9f01",
		"Options.Trim":                                  true,
	} {
		if got := at(task, path); got != want {
			t.Errorf("%s = %#v, want %#v", path, got, want)
		}
	}
	if at(task, "Notification.Ios") != nil || at(task, "Notification.Hmos") != nil {
		t.Errorf("task %v", task)
	}
}

func TestCategories(t *testing.T) {
	p, fake := newProvider(t, config())
	fake.On(api).Reply(outboundtest.JSON(200, map[string]string{}))
	b := batch(push.Android)
	b.Message.Category = "promo"
	if err := p.Push(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	_, task := sent(t, fake)
	if at(task, "Notification.Android.ChannelId") != "promo" ||
		at(task, "Notification.Android.Options.Honor.Importance") != float64(0) ||
		at(task, "Notification.Android.Options.Huawei.Category") != "MARKETING" ||
		at(task, "Notification.Android.Options.Xiaomi") != nil {
		t.Fatalf("task %v", task)
	}

	// 没有配置的分类：不再重试，也不发出请求。
	b.Message.Category = "chat"
	if err := p.Push(context.Background(), b); !queue.IsPermanent(err) {
		t.Fatalf("unknown category: %v", err)
	}

	// 没有配置 default 时，不指定分类的消息不带厂商参数。
	cfg := config()
	delete(cfg.Categories, "default")
	p, fake = newProvider(t, cfg)
	fake.On(api).Reply(outboundtest.JSON(200, map[string]string{}))
	if err := p.Push(context.Background(), batch(push.Android)); err != nil {
		t.Fatal(err)
	}
	if _, task := sent(t, fake); at(task, "Notification.Android.Options") != nil {
		t.Fatalf("task %v", task)
	}
}

func TestIOSAndHarmonyTasks(t *testing.T) {
	cfg := config()
	cfg.APNs = "DEV"
	p, fake := newProvider(t, cfg)
	fake.On(api).Reply(outboundtest.JSON(200, map[string]string{}))
	if err := p.Push(context.Background(), batch(push.IOS)); err != nil {
		t.Fatal(err)
	}
	params, task := sent(t, fake)
	if params["AppKey"] != "335000002" || at(task, "Notification.Ios.ApnsEnv") != "DEV" ||
		at(task, "Notification.Ios.Badge") != float64(3) || at(task, "Notification.Ios.Music") != "default" ||
		at(task, "Notification.Ios.ExtParameters") != `{"article_id":"42"}` || at(task, "Notification.Android") != nil {
		t.Fatalf("ios %v %v", params, task)
	}

	p, fake = newProvider(t, config())
	fake.On(api).Reply(outboundtest.JSON(200, map[string]string{}))
	b := batch(push.HarmonyOS)
	zero := 0
	b.Message.Badge = &zero
	if err := p.Push(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	params, task = sent(t, fake)
	if params["AppKey"] != "335000003" || at(task, "Notification.Hmos.Category") != "WORK" ||
		at(task, "Notification.Hmos.BadgeSetNum") != float64(0) {
		t.Fatalf("harmony %v %v", params, task)
	}
}

func TestErrors(t *testing.T) {
	cfg := config()
	cfg.AppKeys.HarmonyOS = 0
	p, fake := newProvider(t, cfg)
	if err := p.Push(context.Background(), batch(push.HarmonyOS)); !queue.IsPermanent(err) {
		t.Fatalf("no app key: %v", err)
	}

	for name, tc := range map[string]struct {
		reply     outboundtest.Response
		permanent bool
	}{
		"bad access key": {outboundtest.JSON(404, map[string]string{"Code": "InvalidAccessKeyId.NotFound", "Message": "not found", "RequestId": "r1"}), true},
		"bad parameter":  {outboundtest.JSON(400, map[string]string{"Code": "InvalidParameter", "Message": "bad", "RequestId": "r2"}), true},
		"throttled":      {outboundtest.JSON(400, map[string]string{"Code": "Throttling.User", "Message": "slow down"}), false},
		"server error":   {outboundtest.JSON(500, map[string]string{"Code": "InternalError"}), false},
		"bad gateway":    {outboundtest.Text(502, "bad gateway"), false},
		"network":        {outboundtest.Fail(errors.New("connection reset")), false},
	} {
		fake.On(api).Reply(tc.reply)
		err := p.Push(context.Background(), batch(push.Android))
		if err == nil || queue.IsPermanent(err) != tc.permanent {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidate(t *testing.T) {
	two := 2
	for name, edit := range map[string]func(*Config){
		"no access key": func(c *Config) { c.AccessKeySecret = "" },
		"no app keys":   func(c *Config) { c.AppKeys = AppKeys{} },
		"endpoint url":  func(c *Config) { c.Endpoint = "https://cloudpush.aliyuncs.com" },
		"apns":          func(c *Config) { c.APNs = "SANDBOX" },
		"honor":         func(c *Config) { c.Categories["default"] = Category{Honor: &two} },
	} {
		cfg := config()
		edit(&cfg)
		if _, err := New(cfg, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := New(config(), nil); err != nil {
		t.Fatal(err)
	}
}
