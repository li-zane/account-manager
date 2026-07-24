package domain

import (
	"encoding/json"
	"time"
)

const (
	AppSettingKeyTokenRefresh    = "mailbox.token_refresh"
	AppSettingKeyMessageProbe    = "mailbox.message_probe"
	AppSettingKeyBackupScheduler = "backup.scheduler"
)

type AppSetting struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	Version   int64           `json:"version"`
	UpdatedAt time.Time       `json:"updated_at"`
}
