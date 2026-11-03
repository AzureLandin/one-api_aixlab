package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/songquanpeng/one-api/model"
)

// SSOTicket 由管理面板签发的免登票据
// base64url(JSON{u,exp,n}) + "." + base64url(HMAC-SHA256)
type SSOTicket struct {
	Username string `json:"u"`
	ExpireAt int64  `json:"exp"`
	Nonce    string `json:"n"`
}

func ssoSecret() string {
	if s := os.Getenv("SSO_SHARED_SECRET"); s != "" {
		return s
	}
	// 未单独配置时回退到 session secret，保证功能可用但不推荐
	return os.Getenv("SESSION_SECRET")
}

func signSSOTicket(ticket *SSOTicket, secret string) string {
	payload, _ := json.Marshal(ticket)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	sig := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(payload) + "." + sig
}

func parseSSOTicket(ticketStr, secret string) (*SSOTicket, error) {
	parts := strings.Split(ticketStr, ".")
	if len(parts) != 2 {
		return nil, http.ErrHijacked
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	expect, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	actual := h.Sum(nil)
	if subtle.ConstantTimeCompare(expect, actual) != 1 {
		return nil, http.ErrHijacked
	}
	var t SSOTicket
	if err := json.Unmarshal(payload, &t); err != nil {
		return nil, err
	}
	if t.Username == "" || t.ExpireAt == 0 || t.Nonce == "" {
		return nil, http.ErrHijacked
	}
	if time.Now().Unix() > t.ExpireAt {
		return nil, http.ErrHijacked
	}
	return &t, nil
}

// SSOLogin GET /api/sso?ticket=...
// 管理面板签发的免登票据 → 校验 → 建立本域 session → 302 回首页
func SSOLogin(c *gin.Context) {
	secret := ssoSecret()
	if secret == "" {
		c.JSON(http.StatusForbidden, gin.H{"success": false, "message": "SSO 未配置"})
		return
	}
	ticketStr := c.Query("ticket")
	ticket, err := parseSSOTicket(ticketStr, secret)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "SSO 票据无效或已过期"})
		return
	}
	// 防重放：nonce 一次性
	nonce := model.SSOTicketNonce{Nonce: ticket.Nonce, Username: ticket.Username, CreatedAt: time.Now().Unix()}
	if err := model.DB.Create(&nonce).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "SSO 票据已使用"})
		return
	}
	// 顺带清理 24h 前的 nonce
	model.DB.Where("created_at < ?", time.Now().Unix()-86400).Delete(&model.SSOTicketNonce{})

	var user model.User
	err = model.DB.Where("username = ?", ticket.Username).First(&user).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "用户在模型中心不存在"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "查询用户失败"})
		}
		return
	}
	if user.Status != model.UserStatusEnabled {
		c.JSON(http.StatusUnauthorized, gin.H{"success": false, "message": "该账号已被停用"})
		return
	}
	// 建立本域 session cookie（覆盖登出写的主机 cookie），再经 bootstrap 页写入
	// localStorage.user —— SPA 的登录态判定只读 localStorage，不读 cookie。
	session := sessions.Default(c)
	session.Set("id", user.Id)
	session.Set("username", user.Username)
	session.Set("role", user.Role)
	session.Set("status", user.Status)
	if err := session.Save(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "无法保存会话信息"})
		return
	}
	userJSON, _ := json.Marshal(map[string]interface{}{
		"id":           user.Id,
		"username":     user.Username,
		"display_name": user.DisplayName,
		"role":         user.Role,
		"status":       user.Status,
	})
	html := `<!DOCTYPE html><html><head><meta charset="utf-8"><title>登录中…</title></head><body>` +
		`<script>localStorage.setItem('user', ` + string(userJSON) + `); location.replace('/');</script>` +
		`<p>正在进入模型中心…</p></body></html>`
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}
