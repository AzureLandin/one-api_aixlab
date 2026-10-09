package model

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/common/helper"
	"github.com/songquanpeng/one-api/common/logger"
	"gorm.io/gorm"
)

const (
	ChannelStatusUnknown          = 0
	ChannelStatusEnabled          = 1 // don't use 0, 0 is the default value!
	ChannelStatusManuallyDisabled = 2 // also don't use 0
	ChannelStatusAutoDisabled     = 3
)

type Channel struct {
	Id           int     `json:"id"`
	Type         int     `json:"type" gorm:"default:0"`
	Key          string  `json:"key" gorm:"type:text"`
	Status       int     `json:"status" gorm:"default:1"`
	Name         string  `json:"name" gorm:"index"`
	CreatedTime  int64   `json:"created_time" gorm:"bigint"`
	TestTime     int64   `json:"test_time" gorm:"bigint"`
	ResponseTime int     `json:"response_time"` // in milliseconds
	BaseURL      *string `json:"base_url" gorm:"column:base_url;default:''"`
	Models       string  `json:"models"`
	Group        string  `json:"group" gorm:"type:varchar(32);default:'default'"`
	UsedQuota    int64   `json:"used_quota" gorm:"bigint;default:0"`
	ModelMapping *string `json:"model_mapping" gorm:"type:varchar(1024);default:''"`
	Priority     *int64  `json:"priority" gorm:"bigint;default:0"`
	Config       string  `json:"config"`
	SystemPrompt *string `json:"system_prompt" gorm:"type:text"`
}

type ChannelConfig struct {
	Region            string `json:"region,omitempty"`
	SK                string `json:"sk,omitempty"`
	AK                string `json:"ak,omitempty"`
	UserID            string `json:"user_id,omitempty"`
	APIVersion        string `json:"api_version,omitempty"`
	LibraryID         string `json:"library_id,omitempty"`
	Plugin            string `json:"plugin,omitempty"`
	VertexAIProjectID string `json:"vertex_ai_project_id,omitempty"`
	VertexAIADC       string `json:"vertex_ai_adc,omitempty"`
	// ProxyURL routes this channel's upstream calls through an outbound proxy
	// server (http://, https://, socks5:// or socks5h://). Empty means the
	// process-wide default transport is used, so an unconfigured channel is
	// completely unaffected.
	ProxyURL string `json:"proxy_url,omitempty"`
	// ModelConfigs holds optional per-model overrides keyed by the model name
	// (after model mapping). An absent entry — or an absent field inside an
	// entry — means "no override": the upstream/provider default applies.
	ModelConfigs map[string]ModelConfig `json:"model_configs,omitempty"`
	// UpstreamProtocol declares the wire protocol this channel's upstream
	// actually speaks ("openai-chat", "openai-responses",
	// "anthropic-messages" or "compatible"). Empty means "derive it from the
	// channel type". It is validated when the channel is saved: a typo is
	// rejected rather than silently treated as openai-chat, which would take
	// the translation path and drop the native fields (cache_control, thinking,
	// hosted tools) that same-protocol passthrough preserves.
	UpstreamProtocol string `json:"upstream_protocol,omitempty"`
}

// ModelConfig is the optional per-model override block configured on a channel.
// Every field is optional; an unset field is never sent downstream, so the
// model provider's own default stays in effect.
//
// reasoning_effort is deliberately NOT configurable here: the client's own
// value is passed straight through to the upstream without inspection.
type ModelConfig struct {
	// ContextWindow is the maximum total context (input tokens) accepted for
	// this model. 0 disables the check.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is applied as the default max_tokens when the client did
	// not request one. 0 leaves the decision to the provider.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// InputModalities whitelists accepted input modalities ("text", "image",
	// "audio", "file"). Empty means "no restriction".
	InputModalities []string `json:"input_modalities,omitempty"`
}

// GetModelConfig returns the per-model override for the given model name.
// The second result reports whether any override is present, so callers can
// keep the untouched fast path when nothing is configured.
func (cfg ChannelConfig) GetModelConfig(modelName string) (ModelConfig, bool) {
	if len(cfg.ModelConfigs) == 0 || modelName == "" {
		return ModelConfig{}, false
	}
	mc, ok := cfg.ModelConfigs[modelName]
	if !ok {
		return ModelConfig{}, false
	}
	return mc, !mc.IsZero()
}

// IsZero reports whether the override carries no effective setting.
func (mc ModelConfig) IsZero() bool {
	return mc.ContextWindow == 0 &&
		mc.MaxOutputTokens == 0 &&
		len(mc.InputModalities) == 0
}

// AllowsModality reports whether the given input modality is permitted. An
// empty whitelist permits every modality.
func (mc ModelConfig) AllowsModality(modality string) bool {
	if len(mc.InputModalities) == 0 {
		return true
	}
	for _, m := range mc.InputModalities {
		if strings.EqualFold(m, modality) {
			return true
		}
	}
	return false
}

func GetAllChannels(startIdx int, num int, scope string) ([]*Channel, error) {
	var channels []*Channel
	var err error
	switch scope {
	case "all":
		err = DB.Order("id desc").Find(&channels).Error
	case "disabled":
		err = DB.Order("id desc").Where("status = ? or status = ?", ChannelStatusAutoDisabled, ChannelStatusManuallyDisabled).Find(&channels).Error
	default:
		err = DB.Order("id desc").Limit(num).Offset(startIdx).Omit("key").Find(&channels).Error
	}
	return channels, err
}

func SearchChannels(keyword string) (channels []*Channel, err error) {
	err = DB.Omit("key").Where("id = ? or name LIKE ?", helper.String2Int(keyword), keyword+"%").Find(&channels).Error
	return channels, err
}

func GetChannelById(id int, selectAll bool) (*Channel, error) {
	channel := Channel{Id: id}
	var err error = nil
	if selectAll {
		err = DB.First(&channel, "id = ?", id).Error
	} else {
		err = DB.Omit("key").First(&channel, "id = ?", id).Error
	}
	return &channel, err
}

func BatchInsertChannels(channels []Channel) error {
	var err error
	err = DB.Create(&channels).Error
	if err != nil {
		return err
	}
	for _, channel_ := range channels {
		err = channel_.AddAbilities()
		if err != nil {
			return err
		}
	}
	return nil
}

func (channel *Channel) GetPriority() int64 {
	if channel.Priority == nil {
		return 0
	}
	return *channel.Priority
}

func (channel *Channel) GetBaseURL() string {
	if channel.BaseURL == nil {
		return ""
	}
	return *channel.BaseURL
}

func (channel *Channel) GetModelMapping() map[string]string {
	if channel.ModelMapping == nil || *channel.ModelMapping == "" || *channel.ModelMapping == "{}" {
		return nil
	}
	modelMapping := make(map[string]string)
	err := json.Unmarshal([]byte(*channel.ModelMapping), &modelMapping)
	if err != nil {
		logger.SysError(fmt.Sprintf("failed to unmarshal model mapping for channel %d, error: %s", channel.Id, err.Error()))
		return nil
	}
	return modelMapping
}

func (channel *Channel) Insert() error {
	var err error
	err = DB.Create(channel).Error
	if err != nil {
		return err
	}
	err = channel.AddAbilities()
	return err
}

func (channel *Channel) Update() error {
	var err error
	err = DB.Model(channel).Updates(channel).Error
	if err != nil {
		return err
	}
	DB.Model(channel).First(channel, "id = ?", channel.Id)
	err = channel.UpdateAbilities()
	return err
}

func (channel *Channel) UpdateResponseTime(responseTime int64) {
	err := DB.Model(channel).Select("response_time", "test_time").Updates(Channel{
		TestTime:     helper.GetTimestamp(),
		ResponseTime: int(responseTime),
	}).Error
	if err != nil {
		logger.SysError("failed to update response time: " + err.Error())
	}
}

func (channel *Channel) Delete() error {
	var err error
	err = DB.Delete(channel).Error
	if err != nil {
		return err
	}
	err = channel.DeleteAbilities()
	return err
}

func (channel *Channel) LoadConfig() (ChannelConfig, error) {
	var cfg ChannelConfig
	if channel.Config == "" {
		return cfg, nil
	}
	err := json.Unmarshal([]byte(channel.Config), &cfg)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}

func UpdateChannelStatusById(id int, status int) {
	err := UpdateAbilityStatus(id, status == ChannelStatusEnabled)
	if err != nil {
		logger.SysError("failed to update ability status: " + err.Error())
	}
	err = DB.Model(&Channel{}).Where("id = ?", id).Update("status", status).Error
	if err != nil {
		logger.SysError("failed to update channel status: " + err.Error())
	}
}

// LEGACY(阶段2)：渠道 used_quota 不再被新请求写入，保留供旧管理页面读取。
func UpdateChannelUsedQuota(id int, quota int64) {
	if config.BatchUpdateEnabled {
		addNewRecord(BatchUpdateTypeChannelUsedQuota, id, quota)
		return
	}
	updateChannelUsedQuota(id, quota)
}

func updateChannelUsedQuota(id int, quota int64) {
	err := DB.Model(&Channel{}).Where("id = ?", id).Update("used_quota", gorm.Expr("used_quota + ?", quota)).Error
	if err != nil {
		logger.SysError("failed to update channel used quota: " + err.Error())
	}
}

func DeleteChannelByStatus(status int64) (int64, error) {
	result := DB.Where("status = ?", status).Delete(&Channel{})
	return result.RowsAffected, result.Error
}

func DeleteDisabledChannel() (int64, error) {
	result := DB.Where("status = ? or status = ?", ChannelStatusAutoDisabled, ChannelStatusManuallyDisabled).Delete(&Channel{})
	return result.RowsAffected, result.Error
}
