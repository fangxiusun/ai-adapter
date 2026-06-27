package channel

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
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
	logger     *log.Logger
}

type ChannelManager struct {
	channels   map[string]*Channel
	defaultID  string
	logger     *log.Logger
	mu         sync.RWMutex
	database   *db.DB
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

func NewChannelManager(cfgs []config.ChannelConfig, proxies []config.ProxyConfig, logger *log.Logger, database *db.DB) *ChannelManager {
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