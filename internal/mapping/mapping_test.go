package mapping

import (
	"strings"
	"testing"
)

func TestDefaultMappingIsValid(t *testing.T) {
	m, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range m.Servers {
		names[s.Name] = true
		for k, q := range s.Queries {
			if !strings.Contains(q, "namespace, pod") {
				t.Errorf("%s/%s must group by namespace and pod: %s", s.Name, k, q)
			}
		}
	}
	if !names["llamacpp"] || !names["vllm"] {
		t.Fatalf("default mapping should include llamacpp and vllm, got %v", names)
	}
	for _, k := range CommonMetrics {
		if m.Common[k] == "" {
			t.Errorf("common metric %s missing", k)
		}
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"no servers":      `common: {}`,
		"unknown metric":  "servers:\n- name: x\n  detect: up\n  queries:\n    tokens: up",
		"unknown field":   "servers:\n- name: x\n  detect: up\n  typo: 1",
		"duplicate name":  "servers:\n- name: x\n  detect: up\n- name: x\n  detect: up",
		"missing detect":  "servers:\n- name: x",
		"empty query":     "servers:\n- name: x\n  detect: up\n  queries:\n    requests_running: ' '",
		"bad common name": "servers:\n- name: x\n  detect: up\ncommon:\n  cpu: up",
	}
	for name, y := range cases {
		if _, err := Parse([]byte(y)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
