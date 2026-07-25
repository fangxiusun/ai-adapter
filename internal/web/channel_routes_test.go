package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/channel"
	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func TestChannelKeySubroutesUseChannelIDSegment(t *testing.T) {
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	cfg := config.ChannelConfig{
		ID:      "channel-1",
		Enabled: true,
		Keys:    []config.KeyConfig{{Value: "key-one-secret", Name: "key-1"}},
		Retry:   config.RetryConfig{ConsecErrorThreshold: 1, PauseMultiplierSec: 1, PauseMaxSec: 1},
	}
	manager := channel.NewChannelManager([]config.ChannelConfig{cfg}, nil, logger, nil, "priority")
	t.Cleanup(func() {
		manager.Stop()
		logger.Close()
	})
	handler := NewWebHandler(manager, nil, &config.Config{Channels: []config.ChannelConfig{cfg}}, nil, nil, "test")

	batchReq := httptest.NewRequest(http.MethodPost, "/admin/api/channels/channel-1/keys/batch", strings.NewReader(`{"action":"pause","keys":["key-one-secret"]}`))
	batchRec := httptest.NewRecorder()
	handler.handleChannelByID(batchRec, batchReq)
	if batchRec.Code != http.StatusOK {
		t.Fatalf("batch route status = %d; body=%s", batchRec.Code, batchRec.Body.String())
	}
	channelEntry, _ := manager.GetChannel("channel-1")
	if stats := channelEntry.KeyPool().GetStats(); len(stats) != 1 || !stats[0].Paused {
		t.Fatalf("batch pause was not applied: %+v", stats)
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/admin/api/channels/channel-1/keys/export", nil)
	exportRec := httptest.NewRecorder()
	handler.handleChannelByID(exportRec, exportReq)
	if exportRec.Code != http.StatusOK || !strings.Contains(exportRec.Body.String(), "key-one") {
		t.Fatalf("export route failed: status=%d body=%s", exportRec.Code, exportRec.Body.String())
	}

	importReq := httptest.NewRequest(http.MethodPost, "/admin/api/channels/channel-1/keys/import", strings.NewReader("keys:\n  - name: key-2\n    value: key-two-secret\n"))
	importRec := httptest.NewRecorder()
	handler.handleChannelByID(importRec, importReq)
	if importRec.Code != http.StatusOK || channelEntry.KeyPool().Size() != 2 {
		t.Fatalf("import route failed: status=%d size=%d body=%s", importRec.Code, channelEntry.KeyPool().Size(), importRec.Body.String())
	}
}
