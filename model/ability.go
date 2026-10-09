package model

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/utils"
	"github.com/songquanpeng/one-api/relay/upstream"
)

// Channel selection errors. Callers tell them apart so that "this group has no
// such model" and "this group has channels, but none of them can serve the
// protocol this endpoint speaks" do not collapse into one misleading message.
var (
	// ErrChannelNotFound means no enabled channel serves the group and model.
	ErrChannelNotFound = errors.New("channel not found")
	// ErrNoChannelForProtocol means channels exist for the group and model, but
	// none speaks an upstream protocol that can serve the client's.
	ErrNoChannelForProtocol = errors.New("no channel can serve the client protocol")
)

// channelServesClient reports whether a channel speaking proto may be selected
// for a request made with the client protocol client.
//
// upstream.Unsupported as the client protocol constrains nothing: relay modes
// outside the three protocol entry points (embeddings, audio, images, legacy
// completions) reach their adaptor through the chat path, which every upstream
// family serves.
func channelServesClient(client, proto upstream.Protocol) bool {
	if client == upstream.Unsupported {
		return true
	}
	return upstream.CanServe(client, proto)
}

type Ability struct {
	Group     string `json:"group" gorm:"type:varchar(32);primaryKey;autoIncrement:false"`
	Model     string `json:"model" gorm:"primaryKey;autoIncrement:false"`
	ChannelId int    `json:"channel_id" gorm:"primaryKey;autoIncrement:false;index"`
	Enabled   bool   `json:"enabled"`
	Priority  *int64 `json:"priority" gorm:"bigint;default:0;index"`
}

// GetRandomSatisfiedChannel picks a channel for the group and model from the
// database (the path taken when the channel cache is off).
//
// Every candidate is loaded rather than one random row, because the protocol a
// channel speaks lives in its config, which SQL cannot evaluate: eligibility is
// decided here and the random pick happens among the survivors. A group that
// mixes upstream protocols therefore never hands the request to a channel that
// would reject it.
func GetRandomSatisfiedChannel(group string, model string, ignoreFirstPriority bool, client upstream.Protocol) (*Channel, error) {
	groupCol := "`group`"
	trueVal := "1"
	if common.UsingPostgreSQL {
		groupCol = `"group"`
		trueVal = "true"
	}

	abilityQuery := DB.Where(groupCol+" = ? and model = ? and enabled = "+trueVal, group, model)
	if !ignoreFirstPriority {
		maxPrioritySubQuery := DB.Model(&Ability{}).Select("MAX(priority)").Where(groupCol+" = ? and model = ? and enabled = "+trueVal, group, model)
		abilityQuery = abilityQuery.Where("priority = (?)", maxPrioritySubQuery)
	}
	var abilities []Ability
	if err := abilityQuery.Find(&abilities).Error; err != nil {
		return nil, err
	}
	if len(abilities) == 0 {
		return nil, ErrChannelNotFound
	}
	channelIds := make([]int, 0, len(abilities))
	for _, ability := range abilities {
		channelIds = append(channelIds, ability.ChannelId)
	}
	var channels []*Channel
	if err := DB.Where("id IN ?", channelIds).Find(&channels).Error; err != nil {
		return nil, err
	}
	if len(channels) == 0 {
		// Abilities left behind by deleted channels: there is nothing to serve.
		return nil, ErrChannelNotFound
	}
	eligible := make([]*Channel, 0, len(channels))
	for _, channel := range channels {
		if channelServesClient(client, channel.UpstreamProtocol()) {
			eligible = append(eligible, channel)
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoChannelForProtocol
	}
	return eligible[rand.Intn(len(eligible))], nil
}

func (channel *Channel) AddAbilities() error {
	models_ := strings.Split(channel.Models, ",")
	models_ = utils.DeDuplication(models_)
	groups_ := strings.Split(channel.Group, ",")
	abilities := make([]Ability, 0, len(models_))
	for _, model := range models_ {
		for _, group := range groups_ {
			ability := Ability{
				Group:     group,
				Model:     model,
				ChannelId: channel.Id,
				Enabled:   channel.Status == ChannelStatusEnabled,
				Priority:  channel.Priority,
			}
			abilities = append(abilities, ability)
		}
	}
	return DB.Create(&abilities).Error
}

func (channel *Channel) DeleteAbilities() error {
	return DB.Where("channel_id = ?", channel.Id).Delete(&Ability{}).Error
}

// UpdateAbilities updates abilities of this channel.
// Make sure the channel is completed before calling this function.
func (channel *Channel) UpdateAbilities() error {
	// A quick and dirty way to update abilities
	// First delete all abilities of this channel
	err := channel.DeleteAbilities()
	if err != nil {
		return err
	}
	// Then add new abilities
	err = channel.AddAbilities()
	if err != nil {
		return err
	}
	return nil
}

func UpdateAbilityStatus(channelId int, status bool) error {
	return DB.Model(&Ability{}).Where("channel_id = ?", channelId).Select("enabled").Update("enabled", status).Error
}

func GetGroupModels(ctx context.Context, group string) ([]string, error) {
	groupCol := "`group`"
	trueVal := "1"
	if common.UsingPostgreSQL {
		groupCol = `"group"`
		trueVal = "true"
	}
	var models []string
	err := DB.Model(&Ability{}).Distinct("model").Where(groupCol+" = ? and enabled = "+trueVal, group).Pluck("model", &models).Error
	if err != nil {
		return nil, err
	}
	sort.Strings(models)
	return models, err
}

// PlazaAbilityRow is a safe projection for the model plaza: no key, base_url, or config.
type PlazaAbilityRow struct {
	Model     string `gorm:"column:model"`
	ChannelId int    `gorm:"column:channel_id"`
	Priority  int64  `gorm:"column:priority"`
	Name      string `gorm:"column:name"`
	Type      int    `gorm:"column:type"`
	Status    int    `gorm:"column:status"`
}

func GetGroupPlazaRows(group string) ([]PlazaAbilityRow, error) {
	groupCol := "`group`"
	trueVal := "1"
	if common.UsingPostgreSQL {
		groupCol = `"group"`
		trueVal = "true"
	}
	var rows []PlazaAbilityRow
	err := DB.Table("abilities").
		Select("abilities.model, abilities.channel_id, COALESCE(abilities.priority, 0) as priority, channels.name, channels.type, channels.status").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where(fmt.Sprintf("abilities.%s = ? AND abilities.enabled = %s AND channels.status = ?", groupCol, trueVal), group, ChannelStatusEnabled).
		Order("abilities.model ASC, abilities.priority DESC, channels.id ASC").
		Find(&rows).Error
	return rows, err
}
