package workerdomain

import "fmt"

var lifecycleTransitions = map[Lifecycle]map[Lifecycle]struct{}{
	LifecycleIdle: {
		LifecycleStarting: {},
	},
	LifecycleStarting: {
		LifecycleExecuting: {},
		LifecycleFailed:    {},
	},
	LifecycleExecuting: {
		LifecycleIdle:           {},
		LifecyclePaused:         {},
		LifecycleBlocked:        {},
		LifecycleAwaitingReview: {},
		LifecycleFailed:         {},
	},
	LifecycleBlocked: {
		LifecycleExecuting: {},
	},
	LifecyclePaused: {
		LifecycleStarting: {},
		LifecycleIdle:     {},
	},
	LifecycleAwaitingReview: {
		LifecycleIdle: {},
	},
	LifecycleFailed: {
		LifecycleIdle: {},
	},
}

func (l Lifecycle) Valid() bool {
	_, ok := lifecycleTransitions[l]
	return ok
}

func CanTransition(from, to Lifecycle) bool {
	_, ok := lifecycleTransitions[from][to]
	return ok
}

func ValidateTransition(from, to Lifecycle) error {
	if !from.Valid() {
		return fmt.Errorf("unknown current worker lifecycle %q", from)
	}
	if !to.Valid() {
		return fmt.Errorf("unknown target worker lifecycle %q", to)
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("invalid named-worker lifecycle transition %q -> %q", from, to)
	}
	return nil
}
