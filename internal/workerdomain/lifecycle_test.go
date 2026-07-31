package workerdomain

import "testing"

func TestNamedWorkerLifecycleTransitions(t *testing.T) {
	valid := [][2]Lifecycle{
		{LifecycleIdle, LifecycleStarting},
		{LifecycleStarting, LifecycleExecuting},
		{LifecycleStarting, LifecycleFailed},
		{LifecycleExecuting, LifecycleIdle},
		{LifecycleExecuting, LifecycleBlocked},
		{LifecycleExecuting, LifecycleAwaitingReview},
		{LifecycleExecuting, LifecycleFailed},
		{LifecycleBlocked, LifecycleExecuting},
		{LifecycleAwaitingReview, LifecycleIdle},
		{LifecycleFailed, LifecycleIdle},
	}
	for _, transition := range valid {
		if err := ValidateTransition(transition[0], transition[1]); err != nil {
			t.Errorf("ValidateTransition(%q, %q) = %v", transition[0], transition[1], err)
		}
	}
}

func TestNamedWorkerLifecycleRejectsInvalidTransitions(t *testing.T) {
	invalid := [][2]Lifecycle{
		{LifecycleIdle, LifecycleExecuting},
		{LifecycleStarting, LifecycleIdle},
		{LifecycleBlocked, LifecycleIdle},
		{LifecycleAwaitingReview, LifecycleExecuting},
		{LifecycleFailed, LifecycleStarting},
		{Lifecycle("unknown"), LifecycleIdle},
		{LifecycleIdle, Lifecycle("unknown")},
	}
	for _, transition := range invalid {
		if err := ValidateTransition(transition[0], transition[1]); err == nil {
			t.Errorf("ValidateTransition(%q, %q) succeeded", transition[0], transition[1])
		}
	}
}
