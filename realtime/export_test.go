package realtime

import "time"

// SetCredentialInterval 临时修改之后创建的 Realtime 复查凭据的间隔，返回恢复函数。
func SetCredentialInterval(d time.Duration) (restore func()) {
	old := credentialInterval
	credentialInterval = d
	return func() { credentialInterval = old }
}

// Compile 暴露频道模式的编译和匹配。
func Compile(pattern string) (match func(channel string) (Params, bool), err error) {
	c, err := compile(pattern, Public())
	if err != nil {
		return nil, err
	}
	return c.match, nil
}

// NewMemoryPresence 暴露进程内的 PresenceStore。
func NewMemoryPresence() PresenceStore { return newMemoryPresence() }
