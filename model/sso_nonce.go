package model

// SSOTicketNonce 记录已使用过的 SSO 票据 nonce，防止重放
type SSOTicketNonce struct {
	Nonce     string `json:"nonce" gorm:"primaryKey;type:varchar(64)"`
	Username  string `json:"username" gorm:"type:varchar(64)"`
	CreatedAt int64  `json:"created_at" gorm:"index"`
}
