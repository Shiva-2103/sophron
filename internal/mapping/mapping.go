// Package mapping defines which Prometheus queries Sophron runs and what
// each result means. The default mapping is embedded in the binary and can be
// replaced with a file, so new model servers need no code changes.
package mapping

import (
	_ "embed"
	"fmt"
	"os"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

//go:embed default.yaml
var defaultYAML []byte

// Metric names a mapping may use. Each becomes a column in the snapshot store.
const (
	GenerationTokensPerSecond = "generation_tokens_per_second"
	PromptTokensPerSecond     = "prompt_tokens_per_second"
	RequestsRunning           = "requests_running"
	RequestsWaiting           = "requests_waiting"
	KVCacheUsageRatio         = "kv_cache_usage_ratio"
	CPUCores                  = "cpu_cores"
	CPUThrottledRatio         = "cpu_throttled_ratio"
	MemoryWorkingSetBytes     = "memory_working_set_bytes"
	GPUUtilizationRatio       = "gpu_utilization_ratio"
	GPUMemoryUsedBytes        = "gpu_memory_used_bytes"
)

// ServerMetrics are metrics that depend on the model server.
var ServerMetrics = []string{
	GenerationTokensPerSecond, PromptTokensPerSecond, RequestsRunning, RequestsWaiting, KVCacheUsageRatio,
}

// CommonMetrics come from Kubernetes or the GPU exporter, for any server.
var CommonMetrics = []string{
	CPUCores, CPUThrottledRatio, MemoryWorkingSetBytes, GPUUtilizationRatio, GPUMemoryUsedBytes,
}

// Server describes one model server type, such as llama.cpp or vLLM.
type Server struct {
	Name    string            `json:"name"`
	Detect  string            `json:"detect"`
	Queries map[string]string `json:"queries"`
}

// Mapping is the full set of queries.
type Mapping struct {
	Servers []Server          `json:"servers"`
	Common  map[string]string `json:"common"`
}

// Default returns the embedded mapping.
func Default() (*Mapping, error) {
	return Parse(defaultYAML)
}

// Load reads a mapping file, or returns the default when path is empty.
func Load(path string) (*Mapping, error) {
	if path == "" {
		return Default()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mapping file: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates a mapping.
func Parse(data []byte) (*Mapping, error) {
	var m Mapping
	if err := yaml.UnmarshalStrict(data, &m); err != nil {
		return nil, fmt.Errorf("parse mapping: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks names and that every query is non-empty.
func (m *Mapping) Validate() error {
	if len(m.Servers) == 0 {
		return fmt.Errorf("mapping has no servers")
	}
	seen := map[string]bool{}
	for _, s := range m.Servers {
		if s.Name == "" || strings.TrimSpace(s.Detect) == "" {
			return fmt.Errorf("every server needs a name and a detect metric")
		}
		if seen[s.Name] {
			return fmt.Errorf("server %q listed twice", s.Name)
		}
		seen[s.Name] = true
		for k, q := range s.Queries {
			if !slices.Contains(ServerMetrics, k) {
				return fmt.Errorf("server %q: unknown metric %q (allowed: %s)", s.Name, k, strings.Join(ServerMetrics, ", "))
			}
			if strings.TrimSpace(q) == "" {
				return fmt.Errorf("server %q: empty query for %q", s.Name, k)
			}
		}
	}
	for k, q := range m.Common {
		if !slices.Contains(CommonMetrics, k) {
			return fmt.Errorf("common: unknown metric %q (allowed: %s)", k, strings.Join(CommonMetrics, ", "))
		}
		if strings.TrimSpace(q) == "" {
			return fmt.Errorf("common: empty query for %q", k)
		}
	}
	return nil
}
