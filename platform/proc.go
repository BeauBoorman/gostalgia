package platform

// ResourceUsage describes memory and CPU consumption for an OS process.
// If metrics cannot be obtained safely or portably, Supported is false
// and callers must report the metrics as unavailable rather than inventing zeroes.
type ResourceUsage struct {
	Supported   bool   `json:"supported"`
	MemoryBytes uint64 `json:"memory_bytes,omitempty"`
	CPUUserMs   int64  `json:"cpu_user_ms,omitempty"`
	CPUSysMs    int64  `json:"cpu_sys_ms,omitempty"`
}
