package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
)

// parseUsageFilter builds a model.UsageFilter from query parameters.
// When forcedUserID is non-nil (self-service endpoints) the user scope is
// pinned to the caller; admins may filter by user_id/token_id/channel_id/
// model_name/start_timestamp/end_timestamp. usage_cutover_at is always
// applied so legacy-era rows never leak into new Token statistics.
func parseUsageFilter(c *gin.Context, forcedUserID *int) model.UsageFilter {
	f := model.UsageFilter{AfterCutover: true}
	if forcedUserID != nil {
		uid := *forcedUserID
		f.UserID = &uid
	} else if s := c.Query("user_id"); s != "" {
		if id, err := strconv.Atoi(s); err == nil && id > 0 {
			f.UserID = &id
		}
	}
	if s := c.Query("token_id"); s != "" {
		if id, err := strconv.Atoi(s); err == nil && id > 0 {
			f.TokenID = &id
		}
	}
	if s := c.Query("channel_id"); s != "" {
		if id, err := strconv.Atoi(s); err == nil && id > 0 {
			f.ChannelID = &id
		}
	}
	f.ModelName = c.Query("model_name")
	f.Start, _ = strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	f.End, _ = strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	return f
}

func usageFail(c *gin.Context, err error) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": err.Error(),
	})
}

func GetUsageSummary(c *gin.Context) {
	summary(c, nil)
}

func GetSelfUsageSummary(c *gin.Context) {
	uid := c.GetInt(ctxkey.Id)
	summary(c, &uid)
}

func summary(c *gin.Context, forcedUserID *int) {
	row, err := model.SummarizeUsage(parseUsageFilter(c, forcedUserID))
	if err != nil {
		usageFail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    row.Summary(),
	})
}

func GetUsageTrend(c *gin.Context) {
	trend(c, nil)
}

func GetSelfUsageTrend(c *gin.Context) {
	uid := c.GetInt(ctxkey.Id)
	trend(c, &uid)
}

func trend(c *gin.Context, forcedUserID *int) {
	rows, err := model.SummarizeUsageByDay(parseUsageFilter(c, forcedUserID))
	if err != nil {
		usageFail(c, err)
		return
	}
	data := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		data = append(data, gin.H{
			"day":     r.Day,
			"summary": r.Summary(),
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

func usageGrouped(c *gin.Context, dimension string) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	rows, err := model.SummarizeUsageBy(parseUsageFilter(c, nil), dimension, limit)
	if err != nil {
		usageFail(c, err)
		return
	}
	data := make([]gin.H, 0, len(rows))
	for _, r := range rows {
		item := gin.H{"summary": r.Summary()}
		switch dimension {
		case "by-user":
			item["user_id"] = r.UserID
			item["username"] = r.Username
		case "by-model":
			item["model_name"] = r.ModelName
		case "by-channel":
			item["channel_id"] = r.ChannelID
		case "by-token":
			item["token_id"] = r.TokenID
		}
		data = append(data, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    data,
	})
}

func GetUsageByUser(c *gin.Context)    { usageGrouped(c, "by-user") }
func GetUsageByModel(c *gin.Context)   { usageGrouped(c, "by-model") }
func GetUsageByChannel(c *gin.Context) { usageGrouped(c, "by-channel") }
