package proxy

import (
	"time"

	"github.com/fangxiusun/ai-adapter/internal/channel"
)

// traversalChannelState stores request-local progress for one channel. The
// scheduler resets attempted at global round boundaries, not after a channel's
// own keys are exhausted, which produces A/key1 -> B/key1 -> A/key2 ordering.
type traversalChannelState struct {
	ch            *channel.Channel
	attempted     map[string]bool
	cooldownUntil map[string]time.Time
	maxRounds     int
}

type channelTraversal struct {
	states             []*traversalChannelState
	preferredChannelID string
	preferredKey       string
	preferredUsed      bool
}

func newChannelTraversal(candidates []*channel.Channel, preferred successfulRoute, preferredOK bool) *channelTraversal {
	states := make([]*traversalChannelState, 0, len(candidates))
	for _, ch := range candidates {
		maxRounds := ch.Config.Retry.MaxRotationRounds
		if maxRounds <= 0 {
			maxRounds = 1
		}
		states = append(states, &traversalChannelState{
			ch: ch, attempted: make(map[string]bool), cooldownUntil: make(map[string]time.Time), maxRounds: maxRounds,
		})
	}
	traversal := &channelTraversal{states: states}
	if preferredOK {
		traversal.preferredChannelID = preferred.channelID
		traversal.preferredKey = preferred.key
	}
	return traversal
}

func (t *channelTraversal) maxRounds() int {
	maxRounds := 1
	for _, state := range t.states {
		if state.maxRounds > maxRounds {
			maxRounds = state.maxRounds
		}
	}
	return maxRounds
}

func (t *channelTraversal) resetRound() {
	for _, state := range t.states {
		state.attempted = make(map[string]bool)
	}
}

// selectPreferred returns the last successful channel/key pair once. The pair
// is marked attempted in round one so normal traversal resumes with the other
// keys instead of immediately selecting it again.
func (t *channelTraversal) selectPreferred() (*traversalChannelState, *channel.KeyEntry) {
	if t.preferredUsed || t.preferredChannelID == "" || t.preferredKey == "" {
		return nil, nil
	}
	t.preferredUsed = true
	for _, state := range t.states {
		if state.ch.Config.ID != t.preferredChannelID {
			continue
		}
		key := state.ch.KeyPool().FindAvailable(t.preferredKey)
		if key == nil {
			return state, nil
		}
		state.attempted[key.Value] = true
		return state, key
	}
	return nil, nil
}

// selectKey returns one key for this channel in the current global round and
// the earliest request-local cooldown that may make a key eligible later.
func (t *channelTraversal) selectKey(state *traversalChannelState, now time.Time) (*channel.KeyEntry, time.Time) {
	excluded := make(map[string]bool, len(state.attempted)+len(state.cooldownUntil))
	for key := range state.attempted {
		excluded[key] = true
	}
	var earliest time.Time
	for key, until := range state.cooldownUntil {
		if !now.Before(until) {
			delete(state.cooldownUntil, key)
			continue
		}
		excluded[key] = true
		if !state.attempted[key] && (earliest.IsZero() || until.Before(earliest)) {
			earliest = until
		}
	}
	if key := state.ch.KeyPool().NextExcluding(excluded); key != nil {
		state.attempted[key.Value] = true
		return key, earliest
	}
	return nil, earliest
}
