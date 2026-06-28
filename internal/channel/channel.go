package channel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/log"
	"golang.org/x/net/proxy"
)

type Channel struct {
	Config     config.ChannelConfig
	keyPool    *KeyPool
	models     map[string]config.ModelConfig
	httpClient *http.Client
	health     *ChannelHealth
	logger     *log.Logger
}

type ChannelManager struct {
	channels   map[string]*Channel
	defaultID  string
	logger     *log.Logger
	mu         sync.RWMutex
	database   *db.DB
	modelIndex map[string][]*Channel
	balancer   Balancer
}

type ModelInfo struct {
	ID                string
	DisplayName       string
	ContextWindow     int
	MaxOutputTokens   int
	SupportsImages    bool
	SupportsReasoning bool
	Aliases           []string
}

func NewChannelManager(cfgs []config.ChannelConfig, proxies []config.ProxyConfig, logger *log.Logger, database *db.DB, loadBalanceStrategy string) *ChannelManager {
	cm := &ChannelManager{
		channels: make(map[string]*Channel),
		logger:   logger,
		database: database,
	}
	for i, cfg := range cfgs {
		if i == 0 {
			cm.defaultID = cfg.ID
		}
		ch := newChannel(cfg, proxies, logger, database)
		cm.channels[cfg.ID] = ch
	}
	cm.buildModelIndex()
	cm.balancer = NewBalancer(loadBalanceStrategy)
	return cm
}

func newChannel(cfg config.ChannelConfig, proxies []config.ProxyConfig, logger *log.Logger, database *db.DB) *Channel {
	models := make(map[string]config.ModelConfig)
	for _, m := range cfg.Models {
		models[m.ID] = m
		for _, alias := range m.Aliases {
			models[alias] = m
		}
	}
	return &Channel{
		Config:     cfg,
		health:     NewChannelHealth(3, 60),
		keyPool:    NewKeyPool(cfg.Keys, cfg.KeyStrategy, cfg.ID, logger, cfg.Retry.ConsecErrorThreshold, cfg.Retry.PauseMultiplierSec, cfg.Retry.PauseMaxSec, database, time.Duration(cfg.KeyStatsSyncSec)*time.Second),
		models:     models,
		httpClient: buildHTTPClient(cfg, proxies),
		logger:     logger,
	}
}

// buildHTTPClient creates an http.Client with optional proxy support.
func buildHTTPClient(cfg config.ChannelConfig, proxies []config.ProxyConfig) *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout:  60 * time.Second,
		ExpectContinueTimeout:  1 * time.Second,
		MaxIdleConns:           100,
		MaxIdleConnsPerHost:    10,
		IdleConnTimeout:        90 * time.Second,
	}

	// Find and apply proxy if configured
	if cfg.ProxyID != "" {
		for _, p := range proxies {
			if p.ID == cfg.ProxyID {
				applyProxy(transport, p)
				break
			}
		}
	}

	return &http.Client{
		Timeout:   time.Duration(cfg.RequestTimeoutMs) * time.Millisecond,
		Transport: transport,
	}
}

// applyProxy configures the transport to use the given proxy.
func applyProxy(transport *http.Transport, p config.ProxyConfig) {
	switch p.Type {
	case "http":
		proxyURL, err := url.Parse(p.URL)
		if err != nil {
			return
		}
		transport.Proxy = http.ProxyURL(proxyURL)

	case "socks5":
		socksDialer, err := proxy.SOCKS5("tcp", extractHostPort(p.URL), nil, proxy.Direct)
		if err != nil {
			return
		}
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return socksDialer.Dial(network, addr)
		}
	}
}

// extractHostPort parses socks5://user:pass@host:port and returns host:port.
func extractHostPort(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Host
}

func (cm *ChannelManager) SelectChannel(model string) (*Channel, *ModelInfo, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	candidates := cm.modelIndex[model]
	for _, ch := range candidates {
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

func (cm *ChannelManager) Stop() {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	for _, ch := range cm.channels {
		ch.keyPool.Stop()
		ch.keyPool.SaveToDB()
	}
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

// buildModelIndex builds a lookup from modelID to sorted list of channels.
func (cm *ChannelManager) buildModelIndex() {
	cm.modelIndex = make(map[string][]*Channel)
	for _, ch := range cm.channels {
		if !ch.Config.Enabled {
			continue
		}
		models := make(map[string]bool)
		for _, m := range ch.Config.Models {
			models[m.ID] = true
			for _, alias := range m.Aliases {
				models[alias] = true
			}
		}
		for modelID := range models {
			cm.modelIndex[modelID] = append(cm.modelIndex[modelID], ch)
		}
	}
	// Sort by priority (lower number = higher priority)
	for modelID := range cm.modelIndex {
		sort.Slice(cm.modelIndex[modelID], func(i, j int) bool {
			return cm.modelIndex[modelID][i].Config.Priority < cm.modelIndex[modelID][j].Config.Priority
		})
	}
}

// SelectChannelCandidates returns all channels that support the given model, sorted by priority.
func (cm *ChannelManager) SelectChannelCandidates(model string) []*Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	if candidates, ok := cm.modelIndex[model]; ok {
		return candidates
	}

	// Fallback: use default model of default channel
	if ch, ok := cm.channels[cm.defaultID]; ok && ch.Config.Enabled {
		if _, ok := ch.models[ch.Config.DefaultModel]; ok {
			return []*Channel{ch}
		}
	}
	return nil
}

// SelectBalanced selects a single channel from candidates using the configured load balance strategy.
func (cm *ChannelManager) SelectBalanced(candidates []*Channel) *Channel {
	if len(candidates) == 0 {
		return nil
	}
	return cm.balancer.Select(candidates)
}

// ReorderCandidates reorders candidates so that the balanced selection comes first,
// followed by the remaining channels in their original order.
// This ensures failover starts from the balanced-selected channel.
func (cm *ChannelManager) ReorderCandidates(candidates []*Channel) []*Channel {
	if len(candidates) <= 1 {
		return candidates
	}
	selected := cm.balancer.Select(candidates)
	if selected == nil {
		return candidates
	}
	reordered := make([]*Channel, 0, len(candidates))
	reordered = append(reordered, selected)
	for _, ch := range candidates {
		if ch != selected {
			reordered = append(reordered, ch)
		}
	}
	return reordered
}

// IsHealthy returns whether the channel is currently available for requests.
func (ch *Channel) IsHealthy() bool {
	return ch.health.IsHealthy()
}

// ReportChannelSuccess reports a successful request to the channel health tracker.
func (ch *Channel) ReportChannelSuccess() {
	ch.health.ReportSuccess()
}

// ReportChannelFailure reports a failed request (5xx/connection) to the channel health tracker.
func (ch *Channel) ReportChannelFailure() {
	ch.health.ReportFailure()
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

func (ch *Channel) RecordLatency(key string, ms int64) {
	ch.keyPool.RecordLatency(key, ms)
}

func (ch *Channel) ReportError(key string, statusCode int) {
	ch.keyPool.ReportError(key, statusCode)
}

func (ch *Channel) ReportStreamError(key string) {
	ch.keyPool.ReportStreamError(key)
}

func (ch *Channel) HTTPClient() *http.Client {
	return ch.httpClient
}

func (ch *Channel) KeyPool() *KeyPool {
	return ch.keyPool
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