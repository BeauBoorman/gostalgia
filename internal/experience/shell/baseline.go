package shell

import (
	_ "github.com/charmbracelet/bubbles"
)

// Coordinated Charm baseline versions approved and pinned for Gostalgia's
// experience layer (Issue #21). Core runtime, SDK, apps, and CLI tools
// must remain standard-library-only.
const (
	CharmBubbleTeaVersion = "v1.3.10"
	CharmLipGlossVersion  = "v1.1.0"
	CharmBubblesVersion   = "v1.0.0"
)
