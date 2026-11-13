// Package settings 汇总各章节加入的配置段：configuration.md 的基础结构，
// 以及认证、邮件、队列等章节的片段。
package settings

import (
	"example.com/blog/internal/auth"
	"github.com/go-tenon/tenon/crypt"
	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/logging"
	"github.com/go-tenon/tenon/mail"
	"github.com/go-tenon/tenon/mail/resend"
	"github.com/go-tenon/tenon/mail/ses"
	"github.com/go-tenon/tenon/mail/smtp"
	"github.com/go-tenon/tenon/queue"
	"github.com/go-tenon/tenon/schedule"
	"github.com/go-tenon/tenon/telemetry"
)

type Config struct {
	HTTP      httpx.Config     `yaml:"http"`
	Logs      logging.Config   `yaml:"logs"`
	Telemetry telemetry.Config `yaml:"telemetry"`
	Database  database.Config  `yaml:"database"`
	Redis     RedisConfig      `yaml:"redis"`
	Auth      auth.Config      `yaml:"auth"`
	Crypt     crypt.Config     `yaml:"crypt"`
	Mail      MailConfig       `yaml:"mail"`
	Queue     queue.Config     `yaml:"queue"`
	Schedule  schedule.Config  `yaml:"schedule"`
}

type RedisConfig struct {
	URL string `yaml:"url"`
}

type MailConfig struct {
	Driver  string        `yaml:"driver"`
	Editors mail.Address  `yaml:"editors"`
	SMTP    smtp.Config   `yaml:"smtp"`
	Resend  resend.Config `yaml:"resend"`
	SES     ses.Config    `yaml:"ses"`
}
