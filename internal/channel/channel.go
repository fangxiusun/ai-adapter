package channel

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

type Channel struct {
	Config     config.ChannelConfig
	keyPool    *KeyPool
	models     map[string]config.ModelConfig
	httpClient *http.Client
	logger     *log.Logger
}

type ChannelManager struct {
	channels   map[string]*Channel
	defaultID  string
	logger     *log.Logger
	mu         sync.RWMutex
}

type ModelInfo struct {
	ID              string
	DisplayName     string
	ContextWindow   int
	MaxOutputTokens int
	SupportsImages  bool
	SupportsReasoning bool
	Aliases         []string
}

func NewChannelManager(cfgs []config.ChannelConfig, logger *log.Logger) *ChannelManager {
	cm := &ChannelManager{
		channels:  make(map[string]*Channel),
		logger:    logger,
	}
	for i, cfg := range cfgs {
		if i == 0 {
			cm.defaultID = cfg.ID
		}
		ch := newChannel(cfg, logger)
		cm.channels[cfg.ID] = ch
	}
	return cm
}

func newChannel(cfg config.ChannelConfig, logger *log.Logger) *Channel {
	models := make(map[string]config.ModelConfig)
	for _, m := range cfg.Models {
		models[m.ID] = m
		for _, alias := range m.Aliases {
			models[alias] = m
		}
	}
	return &Channel{
		Config:  cfg,
		keyPool: NewKeyPool(cfg.Keys, cfg.KeyStrategy, cfg.ID, logger, cfg.Retry.ConsecErrorThreshold, cfg.Retry.PauseMultiplierSec, cfg.Retry.PauseMaxSec),
		models:  models,
		httpClient: &http.Client{
			Timeout: time.Duration(cfg.RequestTimeoutMs) * time.Millisecond,
		},
		logger: logger,
	}
}

func (cm *ChannelManager) SelectChannel(model string) (*Channel, *ModelInfo, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for _, ch := range cm.channels {
		if !ch.Config.Enabled {
			continue
		}
		if mi, ok := ch.ResolveModel(model); ok {
			return ch, &mi, nil
		}
	}

	if ch, ok := cm.channels[cm.defaultID]; ok && ch.Config.Enabled {
		if mi, ok := ch.ResolveModel(ch.Config.DefaultModel); ok {
			return ch, &mi, nil
		}
	}

	return nil, nil, fmt.Errorf("no channel found for model: %s", model)
}

func (cm *ChannelManager) GetChannel(id string) (*Channel, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	ch, ok := cm.channels[id]
	return ch, ok
}

func (cm *ChannelManager) ListChannels() []*Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	var list []*Channel
	for _, ch := range cm.channels {
		list = append(list, ch)
	}
	return list
}

func (ch *Channel) ResolveModel(clientModel string) (ModelInfo, bool) {
	if m, ok := ch.models[clientModel]; ok {
		return ModelInfo{
			ID:                m.ID,
			DisplayName:       m.DisplayName,
			ContextWindow:     m.ContextWindow,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsImages:    m.SupportsImages,
			SupportsReasoning: m.SupportsReasoning,
			Aliases:           m.Aliases,
		}, true
	}
	if m, ok := ch.models[ch.Config.DefaultModel]; ok {
		return ModelInfo{
			ID:                m.ID,
			DisplayName:       m.DisplayName,
			ContextWindow:     m.ContextWindow,
			MaxOutputTokens:   m.MaxOutputTokens,
			SupportsImages:    m.SupportsImages,
			SupportsReasoning: m.SupportsReasoning,
			Aliases:           m.Aliases,
		}, true
	}
	return ModelInfo{}, false
}

func (ch *Channel) GetKey() *KeyEntry {
	return ch.keyPool.Next()
}

func (ch *Channel) ReportSuccess(key string) {
	ch.keyPool.ReportSuccess(key)
}

func (ch *Channel) ReportError(key string, statusCode int) {
	ch.keyPool.ReportError(key, statusCode)
}

func (ch *Channel) HTTPClient() *http.Client {
	return ch.httpClient
}

func (ch *Channel) KeyPool() *KeyPool {
	return ch.keyPool
}

func (ch *Channel) UpstreamBaseURL() string {
	return ch.Config.BaseURL
}

func (ch *Channel) WireAPI() string {
	return ch.Config.WireAPI
}

func (ch *Channel) MaxRetries() int {
	return ch.Config.MaxRetries
}

func (ch *Channel) RetryDelay() time.Duration {
	return time.Duration(ch.Config.RetryDelayMs) * time.Millisecond
}

func (ch *Channel) FanoutEnabled() bool {
	return ch.Config.Fanout.Enabled
}

func (ch *Channel) FanoutCount() int {
	return ch.Config.Fanout.Count
}

func (ch *Channel) FanoutWaitAll() bool {
	return ch.Config.Fanout.WaitAll
}

