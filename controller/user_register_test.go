package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/model"
)

// setupRegisterTestDB points the model layer at a throwaway database holding
// only the tables the registration path touches.
func setupRegisterTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Token{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	model.DB = db
}

func postRegister(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/user/register", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	Register(c)

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response %q: %v", w.Body.String(), err)
	}
	return out
}

// The happy path: a valid code creates the account, and the password is hashed
// rather than stored in the clear.
func TestRegisterWithValidCodeCreatesUser(t *testing.T) {
	setupRegisterTestDB(t)
	originalEnabled := config.RegisterEnabled
	config.RegisterEnabled = true
	defer func() { config.RegisterEnabled = originalEnabled }()

	const email = "newuser@example.com"
	const code = "123456"
	common.RegisterVerificationCodeWithKey(email, code, common.EmailVerificationPurpose)

	res := postRegister(t, map[string]any{
		"username":          "tester",
		"password":          "password123",
		"email":             email,
		"verification_code": code,
	})
	if res["success"] != true {
		t.Fatalf("registration should succeed, got %v", res)
	}

	var user model.User
	if err := model.DB.Where("username = ?", "tester").First(&user).Error; err != nil {
		t.Fatalf("user should exist: %v", err)
	}
	if user.Email != email {
		t.Fatalf("email want %q, got %q", email, user.Email)
	}
	if user.Password == "password123" {
		t.Fatal("password must be stored hashed, not in clear text")
	}
	if user.Password == "" {
		t.Fatal("password hash must not be empty")
	}
	// A usable account needs the default token the insert path creates.
	var tokenCount int64
	model.DB.Model(&model.Token{}).Where("user_id = ?", user.Id).Count(&tokenCount)
	if tokenCount == 0 {
		t.Fatal("registration should provision a default token")
	}
}

// A code that was never issued (or has expired) must not create anything.
func TestRegisterWithInvalidCodeIsRejected(t *testing.T) {
	setupRegisterTestDB(t)
	originalEnabled := config.RegisterEnabled
	config.RegisterEnabled = true
	defer func() { config.RegisterEnabled = originalEnabled }()

	res := postRegister(t, map[string]any{
		"username":          "nobody",
		"password":          "password123",
		"email":             "nobody@example.com",
		"verification_code": "000000",
	})
	if res["success"] == true {
		t.Fatalf("a bogus code must be rejected, got %v", res)
	}

	var count int64
	model.DB.Model(&model.User{}).Where("username = ?", "nobody").Count(&count)
	if count != 0 {
		t.Fatal("no user may be created when the code is wrong")
	}
}

// Turning registration off must short-circuit before any code check.
func TestRegisterDisabledIsRejected(t *testing.T) {
	setupRegisterTestDB(t)
	originalEnabled := config.RegisterEnabled
	config.RegisterEnabled = false
	defer func() { config.RegisterEnabled = originalEnabled }()

	const email = "disabled@example.com"
	common.RegisterVerificationCodeWithKey(email, "123456", common.EmailVerificationPurpose)

	res := postRegister(t, map[string]any{
		"username":          "blocked",
		"password":          "password123",
		"email":             email,
		"verification_code": "123456",
	})
	if res["success"] == true {
		t.Fatalf("registration must fail when disabled, got %v", res)
	}

	var count int64
	model.DB.Model(&model.User{}).Where("username = ?", "blocked").Count(&count)
	if count != 0 {
		t.Fatal("no user may be created while registration is disabled")
	}
}

// The account signature must match what the modern register form sends, so a
// missing email or code is refused with a clear message rather than a panic.
func TestRegisterRequiresEmailAndCode(t *testing.T) {
	setupRegisterTestDB(t)
	originalEnabled := config.RegisterEnabled
	config.RegisterEnabled = true
	defer func() { config.RegisterEnabled = originalEnabled }()

	res := postRegister(t, map[string]any{
		"username": "nomail",
		"password": "password123",
	})
	if res["success"] == true {
		t.Fatalf("registration without email/code must fail, got %v", res)
	}
}
