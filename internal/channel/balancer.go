package channel

import (
	"math/rand"
	"sort"
	"sync/atomic"
)

// Balancer selects a channel from a list of candidates.
type Balancer interface {
	Select(candidates []*Channel) *Channel
}

// NewBalancer creates a Balancer for the given strategy name.
func NewBalancer(strategy string) Balancer {
	switch strategy {
	case "round-robin":
		return &RoundRobinBalancer{}
	case "random":
		return &RandomBalancer{}
	default:
		return &PriorityBalancer{}
	}
}

// PriorityBalancer selects the first healthy channel in priority order.
type PriorityBalancer struct{}

func (b *PriorityBalancer) Select(candidates []*Channel) *Channel {
	for _, ch := range candidates {
		if ch.IsHealthy() {
			return ch
		}
	}
	// All unhealthy — return the first one as last resort
	if len(candidates) > 0 {
		return candidates[0]
	}
	return nil
}

// RoundRobinBalancer round-robins among channels of the same lowest priority.
type RoundRobinBalancer struct {
	counter uint64
}

func (b *RoundRobinBalancer) Select(candidates []*Channel) *Channel {
	if len(candidates) == 0 {
		return nil
	}

	// Group by priority
	grouped := groupByPriority(candidates)
	// Find lowest priority group
	group := grouped[0].channels

	// Round-robin within the group
	idx := atomic.AddUint64(&b.counter, 1) - 1
	ch := group[idx%uint64(len(group))]

	// If selected channel is unhealthy, fall back to priority order
	if !ch.IsHealthy() {
		for _, g := range grouped {
			for _, c := range g.channels {
				if c.IsHealthy() {
					return c
				}
			}
		}
	}
	return ch
}

// RandomBalancer randomly selects among channels of the same lowest priority.
type RandomBalancer struct{}

func (b *RandomBalancer) Select(candidates []*Channel) *Channel {
	if len(candidates) == 0 {
		return nil
	}

	grouped := groupByPriority(candidates)
	group := grouped[0].channels

	// Filter healthy channels
	var healthy []*Channel
	for _, ch := range group {
		if ch.IsHealthy() {
			healthy = append(healthy, ch)
		}
	}
	if len(healthy) > 0 {
		return healthy[rand.Intn(len(healthy))]
	}
	// All unhealthy — return first as last resort
	return group[0]
}

type priorityGroup struct {
	priority int
	channels []*Channel
}

func groupByPriority(candidates []*Channel) []priorityGroup {
	groups := make(map[int][]*Channel)
	for _, ch := range candidates {
		groups[ch.Config.Priority] = append(groups[ch.Config.Priority], ch)
	}
	var sorted []priorityGroup
	for p, chs := range groups {
		sorted = append(sorted, priorityGroup{priority: p, channels: chs})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].priority < sorted[j].priority
	})
	return sorted
}
