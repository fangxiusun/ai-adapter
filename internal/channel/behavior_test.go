package channel

import (
	"path/filepath"
	"testing"

	"github.com/fangxiusun/ai-adapter/internal/config"
	"github.com/fangxiusun/ai-adapter/internal/db"
	"github.com/fangxiusun/ai-adapter/internal/log"
)

func TestChannelOrderingIsStable(t *testing.T) {
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	manager := NewChannelManager([]config.ChannelConfig{
		{ID: "b", Enabled: true, Priority: 10, Models: []config.ModelConfig{{ID: "m"}}},
		{ID: "a", Enabled: true, Priority: 10, Models: []config.ModelConfig{{ID: "m"}}},
		{ID: "c", Enabled: true, Priority: 5, Models: []config.ModelConfig{{ID: "m"}}},
	}, nil, logger, nil, "priority")
	t.Cleanup(func() {
		manager.Stop()
		logger.Close()
	})

	assertChannelIDs(t, manager.ListChannels(), []string{"c", "a", "b"})
	assertChannelIDs(t, manager.SelectChannelCandidates("m"), []string{"c", "a", "b"})
}

func TestGetNRejectsNonPositiveCount(t *testing.T) {
	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	pool := NewKeyPool([]config.KeyConfig{{Value: "key-1"}}, "round-robin", "test", logger, 1, 1, 1, nil, 0)
	t.Cleanup(func() {
		pool.Stop()
		logger.Close()
	})
	if keys := pool.GetN(-1); len(keys) != 0 {
		t.Fatalf("GetN(-1) returned %d keys, want 0", len(keys))
	}
}

func TestPermanentSkipPersistsAcrossKeyPoolReload(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "key-stats.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()

	logger := log.New("error", "", false, false)
	logger.SetEnabled(false)
	defer logger.Close()
	keys := []config.KeyConfig{{Value: "key-1", Name: "key-1"}}
	first := NewKeyPool(keys, "round-robin", "channel-1", logger, 1, 1, 1, database, 0)
	first.ReportError("key-1", 401)
	first.SaveToDB()
	first.Stop()

	second := NewKeyPool(keys, "round-robin", "channel-1", logger, 1, 1, 1, database, 0)
	defer second.Stop()
	if key := second.Next(); key != nil {
		t.Fatalf("permanently skipped key became selectable after reload: %s", key.Value)
	}
	stats := second.GetStats()
	if len(stats) != 1 || !stats[0].PermanentlySkipped || stats[0].Error401 != 1 {
		t.Fatalf("unexpected restored state: %+v", stats)
	}
}

func assertChannelIDs(t *testing.T, channels []*Channel, want []string) {
	t.Helper()
	if len(channels) != len(want) {
		t.Fatalf("channel count = %d, want %d", len(channels), len(want))
	}
	for i, id := range want {
		if channels[i].Config.ID != id {
			t.Fatalf("channel[%d] = %s, want %s", i, channels[i].Config.ID, id)
		}
	}
}
