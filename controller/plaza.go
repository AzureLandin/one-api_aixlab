package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/songquanpeng/one-api/common/ctxkey"
	"github.com/songquanpeng/one-api/model"
)

// 阶段2：模型广场不再展示倍率与费用（quota/费用字段已从 API 移除）。
// 广场只回答一个问题：我这个分组能用到哪些模型、由哪些渠道提供服务。

type plazaChannel struct {
	Id       int    `json:"id"`
	Name     string `json:"name"`
	Type     int    `json:"type"`
	Status   int    `json:"status"`
	Priority int64  `json:"priority"`
}

type plazaModel struct {
	Name     string         `json:"name"`
	Channels []plazaChannel `json:"channels"`
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

	models := make([]*plazaModel, 0)
	indexByName := make(map[string]*plazaModel)

	for _, row := range rows {
		ch := plazaChannel{
			Id:       row.ChannelId,
			Name:     row.Name,
			Type:     row.Type,
			Status:   row.Status,
			Priority: row.Priority,
		}

		item, ok := indexByName[row.Model]
		if !ok {
			item = &plazaModel{
				Name:     row.Model,
				Channels: []plazaChannel{ch},
			}
			indexByName[row.Model] = item
			models = append(models, item)
			continue
		}
		item.Channels = append(item.Channels, ch)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"group":  userGroup,
			"models": models,
		},
	})
}
