package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
	relaymodel "github.com/songquanpeng/one-api/relay/model"
)

// https://github.com/songquanpeng/one-api/issues/79
// 以下是 OpenAI 额度查询接口（/v1/dashboard/billing/*）的响应体。
// 阶段2：本系统不再使用额度结算，这两个端点退化为纯协议兼容占位，
// 用于让 OpenAI 生态客户端正常完成初始化检查；返回值不代表任何真实
// 计量数据，也不再读取 quota 列。

type OpenAISubscriptionResponse struct {
	Object             string  `json:"object"`
	HasPaymentMethod   bool    `json:"has_payment_method"`
	SoftLimitUSD       float64 `json:"soft_limit_usd"`
	HardLimitUSD       float64 `json:"hard_limit_usd"`
	SystemHardLimitUSD float64 `json:"system_hard_limit_usd"`
	AccessUntil        int64   `json:"access_until"`
}

type OpenAIUsageResponse struct {
	Object string `json:"object"`
	//DailyCosts []OpenAIUsageDailyCost `json:"daily_costs"`
	TotalUsage float64 `json:"total_usage"` // unit: 0.01 dollar
}

// GetSubscription 返回固定的“不限额”占位；仅 AccessUntil 仍取令牌有效期
// （有效期属于权限语义，不是额度，保留）。
func GetSubscription(c *gin.Context) {
	var accessUntil int64
	var err error
	if config.DisplayTokenStatEnabled {
		tokenId := c.GetInt(ctxkey.TokenId)
		var token *model.Token
		token, err = model.GetTokenById(tokenId)
		if err == nil && token.ExpiredTime > 0 {
			accessUntil = token.ExpiredTime
		}
	}
	if err != nil {
		Error := relaymodel.Error{
			Message: err.Error(),
			Type:    "one_api_error",
		}
		c.JSON(200, gin.H{
			"error": Error,
		})
		return
	}
	// 占位上限，语义为“无限制”，与任何计量无关
	const unlimited = 100000000
	c.JSON(200, OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       unlimited,
		HardLimitUSD:       unlimited,
		SystemHardLimitUSD: unlimited,
		AccessUntil:        accessUntil,
	})
	return
}

// GetUsage 返回零占位。真实用量请查询 /api/usage/*（阶段 1 事实层）。
func GetUsage(c *gin.Context) {
	c.JSON(200, OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: 0,
	})
	return
}
