package plugins

import (
	"github.com/vinodhalaharvi/silt/plan"
	"github.com/vinodhalaharvi/silt/plugin"
)

// The ESPHome kind.
//
// A claim here is a path and a value - mqtt.topic_prefix, switch.toggle.pin -
// which is a different type from a Kconfig constraint, so a plan built for one
// cannot be run against the other's interpreter.
//
// What this kind cannot do is as much of its definition as what it can. It has
// no Settle, because an ESPHome document has no defaults silt models: a path
// nobody states is a path the firmware will not carry. It has no Solve. And
// its Emit names no Buildroot symbol, because esphome builds the result on
// another processor and Buildroot has nothing to hand it to - which was an
// error demanding a route that could not exist, before the kinds were
// separated.

type ESPHomeCfg struct {
	// Stated is every path the composition sets, which is the whole of what
	// this kind knows until a pinned schema is read.
	Stated map[string]string
}

var ESPHome = plugin.Interp[PathClaim, ESPHomeCfg, ESPHomeCfg]{
	Open: func(c ESPHomeCfg) (ESPHomeCfg, error) { return c, nil },

	Answer: func(c ESPHomeCfg, k plan.Ask[PathClaim]) plan.Answer {
		switch k.Op {
		case plan.Settled:
			v, ok := c.Stated[k.Name]
			return settled(v, ok)
		case plan.Exists:
			_, ok := c.Stated[k.Name]
			return plan.Yes(ok)
		}
		// Holds needs ESPHome's published schema, which is pinned per release
		// the way Buildroot is. Until that is read, a path is carried rather
		// than checked - and saying so is better than passing it.
		return plan.NotAnswerable()
	},
}
