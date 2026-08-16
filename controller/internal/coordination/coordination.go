// Package coordination owns locks shared across controller service boundaries.
package coordination

import "sync"

// UpstreamBindings serializes subscription binding mutations with registry
// group deletion so validation and reference checks cannot race.
type UpstreamBindings struct {
	sync.Mutex
}
