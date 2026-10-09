package model

import (
	"errors"
	"fmt"
	"testing"

	"github.com/songquanpeng/one-api/common"
	"github.com/songquanpeng/one-api/common/config"
	"github.com/songquanpeng/one-api/relay/channeltype"
	"github.com/songquanpeng/one-api/relay/upstream"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupChannelSelectDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	DB = db
	common.UsingSQLite = true
	common.UsingPostgreSQL = false
	if err := db.AutoMigrate(&Channel{}, &Ability{}); err != nil {
		t.Fatalf("legacy tables: %v", err)
	}
}

// newSelectChannel inserts an enabled channel together with its abilities, the
// way the admin API does.
func newSelectChannel(t *testing.T, id int, channelType int, channelConfig string, priority int64) {
	t.Helper()
	p := priority
	channel := &Channel{
		Id:       id,
		Type:     channelType,
		Name:     fmt.Sprintf("select-ch-%d", id),
		Group:    "default",
		Models:   "gpt-test",
		Status:   ChannelStatusEnabled,
		Priority: &p,
		Config:   channelConfig,
	}
	if err := channel.Insert(); err != nil {
		t.Fatalf("insert channel %d: %v", id, err)
	}
}

// drawSelectedChannels samples the selection repeatedly so that a test can see
// which channels are reachable rather than which one happened to come first.
func drawSelectedChannels(t *testing.T, times int, client upstream.Protocol) map[int]bool {
	t.Helper()
	seen := make(map[int]bool, times)
	for i := 0; i < times; i++ {
		channel, err := CacheGetRandomSatisfiedChannel("default", "gpt-test", false, client)
		if err != nil {
			t.Fatalf("select for %s: %v", client, err)
		}
		seen[channel.Id] = true
	}
	return seen
}

// A group that mixes upstream protocols must not hand a protocol entry point a
// channel that cannot serve it. Gemini's family is not translated, so it stays
// reachable from the chat path and is excluded from /v1/responses and
// /v1/messages. Run against both selection paths, which must agree.
func TestChannelSelectionFiltersByClientProtocol(t *testing.T) {
	for _, memoryCache := range []bool{false, true} {
		name := "database"
		if memoryCache {
			name = "memory-cache"
		}
		t.Run(name, func(t *testing.T) {
			setupChannelSelectDB(t)
			previous := config.MemoryCacheEnabled
			config.MemoryCacheEnabled = memoryCache
			t.Cleanup(func() { config.MemoryCacheEnabled = previous })

			newSelectChannel(t, 1, channeltype.Gemini, "", 0)
			newSelectChannel(t, 2, channeltype.OpenAI, "", 0)
			if memoryCache {
				InitChannelCache()
			}

			seen := drawSelectedChannels(t, 40, upstream.OpenAIResponses)
			if !seen[2] || seen[1] {
				t.Fatalf("responses client must only reach the OpenAI channel, got %v", seen)
			}
			seen = drawSelectedChannels(t, 40, upstream.AnthropicMessages)
			if !seen[2] || seen[1] {
				t.Fatalf("messages client must only reach the OpenAI channel, got %v", seen)
			}
			seen = drawSelectedChannels(t, 40, upstream.OpenAIChat)
			if !seen[1] || !seen[2] {
				t.Fatalf("chat client must reach both channels, got %v", seen)
			}
		})
	}
}

// The tier is chosen among the usable channels: a higher-priority channel that
// cannot serve the client protocol must not shadow a lower-priority one that
// can, and filtering the top tier away must not leave an empty draw.
func TestChannelSelectionComputesPriorityAfterFiltering(t *testing.T) {
	setupChannelSelectDB(t)
	// Highest priority, but cannot serve a protocol entry point.
	newSelectChannel(t, 1, channeltype.Gemini, "", 10)
	newSelectChannel(t, 2, channeltype.OpenAI, "", 1)
	newSelectChannel(t, 3, channeltype.OpenAI, "", 10)

	seen := drawSelectedChannels(t, 30, upstream.OpenAIResponses)
	if !seen[3] || seen[1] || seen[2] {
		t.Fatalf("want only the top-priority usable channel 3, got %v", seen)
	}

	// Ignoring the first priority tier now walks the remaining usable tier
	// only; the excluded channel 1 must never appear.
	seen = map[int]bool{}
	for i := 0; i < 30; i++ {
		channel, err := CacheGetRandomSatisfiedChannel("default", "gpt-test", true, upstream.OpenAIResponses)
		if err != nil {
			t.Fatalf("select ignoring first priority: %v", err)
		}
		seen[channel.Id] = true
	}
	if seen[1] || len(seen) == 0 {
		t.Fatalf("excluded channel must never be selected, got %v", seen)
	}
}

// A relay mode outside the three protocol entry points (embeddings, audio,
// images, legacy completions) takes the chat path, where every adaptor is
// reachable, so it constrains nothing.
func TestChannelSelectionUnconstrainedClientKeepsUntranslatedChannel(t *testing.T) {
	setupChannelSelectDB(t)
	newSelectChannel(t, 1, channeltype.Gemini, "", 0)

	channel, err := CacheGetRandomSatisfiedChannel("default", "gpt-test", false, upstream.Unsupported)
	if err != nil {
		t.Fatalf("unconstrained select: %v", err)
	}
	if channel.Id != 1 {
		t.Fatalf("want channel 1, got %d", channel.Id)
	}
}

// An explicit protocol in the channel config wins over the one implied by the
// channel type, so an admin can declare that a non-OpenAI channel type is
// actually reached over a translated protocol.
func TestChannelSelectionHonoursConfiguredUpstreamProtocol(t *testing.T) {
	setupChannelSelectDB(t)
	newSelectChannel(t, 1, channeltype.Gemini, `{"upstream_protocol":"openai-chat"}`, 0)

	channel, err := CacheGetRandomSatisfiedChannel("default", "gpt-test", false, upstream.OpenAIResponses)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if channel.Id != 1 {
		t.Fatalf("want channel 1, got %d", channel.Id)
	}
	if got := channel.UpstreamProtocol(); got != upstream.OpenAIChat {
		t.Fatalf("resolved protocol = %q, want %q", got, upstream.OpenAIChat)
	}
}

// "no such model in this group" and "channels exist but none can serve this
// protocol" are different failures and must not collapse into one message.
func TestChannelSelectionErrorKinds(t *testing.T) {
	for _, memoryCache := range []bool{false, true} {
		name := "database"
		if memoryCache {
			name = "memory-cache"
		}
		t.Run(name, func(t *testing.T) {
			setupChannelSelectDB(t)
			previous := config.MemoryCacheEnabled
			config.MemoryCacheEnabled = memoryCache
			t.Cleanup(func() { config.MemoryCacheEnabled = previous })
			if memoryCache {
				InitChannelCache()
			}

			if _, err := CacheGetRandomSatisfiedChannel("default", "gpt-test", false, upstream.OpenAIChat); !errors.Is(err, ErrChannelNotFound) {
				t.Fatalf("empty group: want ErrChannelNotFound, got %v", err)
			}

			newSelectChannel(t, 1, channeltype.Gemini, "", 0)
			if memoryCache {
				InitChannelCache()
			}
			if _, err := CacheGetRandomSatisfiedChannel("default", "gpt-test", false, upstream.AnthropicMessages); !errors.Is(err, ErrNoChannelForProtocol) {
				t.Fatalf("ineligible group: want ErrNoChannelForProtocol, got %v", err)
			}
		})
	}
}
