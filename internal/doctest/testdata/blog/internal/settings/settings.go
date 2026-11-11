// Package settings 汇总各章节加入的配置段：configuration.md 的基础结构，
// 以及认证、邮件、队列等章节的片段。
package settings

import (
	"example.com/blog/internal/auth"
	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/mail"
	"github.com/ryan053857/mygoravel/mail/resend"
	"github.com/ryan053857/mygoravel/mail/smtp"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/schedule"
)

type Config struct {
	HTTP     httpx.Config    `yaml:"http"`
	Logs     logging.Config  `yaml:"logs"`
	Database database.Config `yaml:"database"`
	Auth     auth.Config     `yaml:"auth"`
	Crypt    crypt.Config    `yaml:"crypt"`
	Mail     MailConfig      `yaml:"mail"`
	Queue    queue.Config    `yaml:"queue"`
	Schedule schedule.Config `yaml:"schedule"`
}

type MailConfig struct {
	Driver  string        `yaml:"driver"`
	Editors mail.Address  `yaml:"editors"`
	SMTP    smtp.Config   `yaml:"smtp"`
	Resend  resend.Config `yaml:"resend"`
}
