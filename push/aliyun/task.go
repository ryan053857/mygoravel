package aliyun

// 以下是 PushV2 的 PushTask 中本包用到的字段，名称与接口文档一致：
// https://help.aliyun.com/zh/document_detail/2979224.html

type pushTask struct {
	Target       target       `json:"Target"`
	Notification notification `json:"Notification"`
	Options      options      `json:"Options"`
}

type target struct {
	Type  string `json:"Type"`
	Value string `json:"Value"`
}

type notification struct {
	Title     string               `json:"Title"`
	Body      string               `json:"Body"`
	Android   *androidNotification `json:"Android,omitempty"`
	IOS       *iosNotification     `json:"Ios,omitempty"`
	HarmonyOS *harmonyNotification `json:"Hmos,omitempty"`
}

type androidNotification struct {
	VendorChannelActivity string          `json:"VendorChannelActivity,omitempty"`
	ChannelID             string          `json:"ChannelId,omitempty"`
	ExtParameters         string          `json:"ExtParameters,omitempty"`
	BadgeSetNum           int             `json:"BadgeSetNum,omitempty"`
	Options               *androidOptions `json:"Options,omitempty"`
}

type androidOptions struct {
	Huawei *vendorCategory `json:"Huawei,omitempty"`
	Honor  *honorOptions   `json:"Honor,omitempty"`
	Vivo   *vendorCategory `json:"Vivo,omitempty"`
	Oppo   *vendorCategory `json:"Oppo,omitempty"`
	Xiaomi *xiaomiOptions  `json:"Xiaomi,omitempty"`
}

type vendorCategory struct {
	Category string `json:"Category"`
}

type honorOptions struct {
	Importance int `json:"Importance"`
}

type xiaomiOptions struct {
	Channel string `json:"Channel"`
}

type iosNotification struct {
	ApnsEnv       string `json:"ApnsEnv"`
	Music         string `json:"Music"`
	Badge         *int   `json:"Badge,omitempty"`
	ExtParameters string `json:"ExtParameters,omitempty"`
}

type harmonyNotification struct {
	Category      string `json:"Category,omitempty"`
	BadgeSetNum   *int   `json:"BadgeSetNum,omitempty"`
	ExtParameters string `json:"ExtParameters,omitempty"`
}

type options struct {
	ExpireTime string `json:"ExpireTime,omitempty"`
	JobKey     string `json:"JobKey,omitempty"`
	Trim       bool   `json:"Trim,omitempty"`
}
