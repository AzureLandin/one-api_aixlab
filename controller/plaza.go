package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
	billingratio "github.com/songquanpeng/one-api/relay/billing/ratio"
)

type plazaChannel struct {
	Id               int     `json:"id"`
	Name             string  `json:"name"`
	Type             int     `json:"type"`
	Status           int     `json:"status"`
	Priority         int64   `json:"priority"`
	ModelRatio       float64 `json:"model_ratio"`
	CompletionRatio  float64 `json:"completion_ratio"`
	InputQuotaPer1k  float64 `json:"input_quota_per_1k"`
	OutputQuotaPer1k float64 `json:"output_quota_per_1k"`
}

type plazaModel struct {
	Name             string         `json:"name"`
	ModelRatio       float64        `json:"model_ratio"`
	CompletionRatio  float64        `json:"completion_ratio"`
	GroupRatio       float64        `json:"group_ratio"`
	InputQuotaPer1k  float64        `json:"input_quota_per_1k"`
	OutputQuotaPer1k float64        `json:"output_quota_per_1k"`
	RatioConfigured  bool           `json:"ratio_configured"`
	RatioVaries      bool           `json:"ratio_varies"`
	Channels         []plazaChannel `json:"channels"`
}

func quotaPer1k(groupRatio, modelRatio, completionRatio float64) (input float64, output float64) {
	input = 1000 * groupRatio * modelRatio
	output = input * completionRatio
	return
}

func GetPlazaModels(c *gin.Context) {
	userId := c.GetInt(ctxkey.Id)
	userGroup, err := model.CacheGetUserGroup(userId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	rows, err := model.GetGroupPlazaRows(userGroup)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	groupRatio := billingratio.GetGroupRatio(userGroup)
	models := make([]*plazaModel, 0)
	indexByName := make(map[string]*plazaModel)

	for _, row := range rows {
		modelRatio := billingratio.GetModelRatio(row.Model, row.Type)
		completionRatio := billingratio.GetCompletionRatio(row.Model, row.Type)
		inputQuota, outputQuota := quotaPer1k(groupRatio, modelRatio, completionRatio)
		ch := plazaChannel{
			Id:               row.ChannelId,
			Name:             row.Name,
			Type:             row.Type,
			Status:           row.Status,
			Priority:         row.Priority,
			ModelRatio:       modelRatio,
			CompletionRatio:  completionRatio,
			InputQuotaPer1k:  inputQuota,
			OutputQuotaPer1k: outputQuota,
		}

		item, ok := indexByName[row.Model]
		if !ok {
			item = &plazaModel{
				Name:             row.Model,
				ModelRatio:       modelRatio,
				CompletionRatio:  completionRatio,
				GroupRatio:       groupRatio,
				InputQuotaPer1k:  inputQuota,
				OutputQuotaPer1k: outputQuota,
				RatioConfigured:  billingratio.IsModelRatioConfigured(row.Model, row.Type),
				RatioVaries:      false,
				Channels:         []plazaChannel{ch},
			}
			indexByName[row.Model] = item
			models = append(models, item)
			continue
		}
		item.Channels = append(item.Channels, ch)
		if ch.ModelRatio != item.ModelRatio || ch.CompletionRatio != item.CompletionRatio {
			item.RatioVaries = true
		}
		if billingratio.IsModelRatioConfigured(row.Model, row.Type) {
			item.RatioConfigured = true
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"group":               userGroup,
			"group_ratio":         groupRatio,
			"quota_per_unit":      config.QuotaPerUnit,
			"display_in_currency": config.DisplayInCurrencyEnabled,
			"models":              models,
		},
	})
}
