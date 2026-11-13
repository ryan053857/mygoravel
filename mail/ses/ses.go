// Package ses 通过 Amazon SES 发送邮件，底层使用 github.com/aws/aws-sdk-go-v2/service/sesv2。
package ses

import (
	"bytes"
	"context"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/mail"
	"github.com/go-tenon/tenon/mail/internal/mimemsg"
)

// Config 是 SES 发送配置。AccessKey 为空时使用 AWS 默认凭证链（环境变量、共享配置、
// EC2/ECS/EKS 角色）；Region 为空时同样从默认配置读取。
type Config struct {
	From             mail.Address `yaml:"from"` // 默认发件人，域名或地址需要在 SES 中验证
	Region           string       `yaml:"region"`
	AccessKey        string       `yaml:"access_key"`
	SecretKey        string       `yaml:"secret_key"`
	ConfigurationSet string       `yaml:"configuration_set"` // 可选，用于事件发布和专用 IP 池
}

// Validate 检查配置完整。
func (c Config) Validate() error {
	var errs []error
	if err := c.From.Validate(); err != nil {
		errs = append(errs, errors.Wrap(err, "from"))
	}
	if (strings.TrimSpace(c.AccessKey) == "") != (strings.TrimSpace(c.SecretKey) == "") {
		errs = append(errs, errors.New("access_key and secret_key must be set together"))
	}
	return errors.Join(errs...)
}

// Mailer 通过 SES 发送邮件，可并发使用。
type Mailer struct {
	client           *sesv2.Client
	from             mail.Address
	configurationSet string
}

var _ mail.Mailer = (*Mailer)(nil)

// New 加载 AWS 配置并创建 SES 客户端，不访问网络。凭证在第一次发送时才解析。
func New(cfg Config) (*Mailer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "ses")
	}
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, errors.Wrap(err, "ses: load config")
	}
	if awsCfg.Region == "" {
		return nil, errors.New("ses: region is required")
	}
	m, err := NewWithClient(sesv2.NewFromConfig(awsCfg), cfg.From)
	if err != nil {
		return nil, err
	}
	m.configurationSet = cfg.ConfigurationSet
	return m, nil
}

// NewWithClient 使用应用自己创建的 SDK 客户端，例如自定义 http.Client 或端点。
func NewWithClient(client *sesv2.Client, from mail.Address) (*Mailer, error) {
	if client == nil {
		return nil, errors.New("ses: client is required")
	}
	if err := from.Validate(); err != nil {
		return nil, errors.Wrap(err, "ses: from")
	}
	return &Mailer{client: client, from: from}, nil
}

// Client 返回底层 SDK 客户端，用于 SES 特有的功能，如模板、批量发送、抑制列表。
func (m *Mailer) Client() *sesv2.Client { return m.client }

// Send 校验邮件，生成 MIME 原文，再调用 SES 的发送接口。
// 收件人（包括 Bcc）放在 Destination 中，Bcc 不会出现在邮件头里。
func (m *Mailer) Send(ctx context.Context, msg mail.Message) error {
	msg = msg.WithDefaultFrom(m.from)
	if err := msg.Validate(); err != nil {
		return err
	}
	gm, err := mimemsg.Build(msg)
	if err != nil {
		return errors.Wrap(err, "ses: build message")
	}
	var raw bytes.Buffer
	if _, err := gm.WriteTo(&raw); err != nil {
		return errors.Wrap(err, "ses: build message")
	}
	in := &sesv2.SendEmailInput{
		Content: &types.EmailContent{Raw: &types.RawMessage{Data: raw.Bytes()}},
		Destination: &types.Destination{
			ToAddresses:  emails(msg.To),
			CcAddresses:  emails(msg.Cc),
			BccAddresses: emails(msg.Bcc),
		},
	}
	if m.configurationSet != "" {
		in.ConfigurationSetName = aws.String(m.configurationSet)
	}
	if _, err := m.client.SendEmail(ctx, in); err != nil {
		return errors.Wrap(err, "ses: send")
	}
	return nil
}

func emails(list []mail.Address) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.Email
	}
	return out
}
