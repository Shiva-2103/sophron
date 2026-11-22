// Package cost turns node prices into model costs: what each model pod costs
// per hour, per million tokens, and how much of each node sits idle.
//
// The method follows OpenCost: a node's hourly price is split into a price
// per CPU core, per GiB of memory and per GPU, and each pod pays for the
// larger of what it reserved and what it used. Node cost that no pod
// reserved is idle cost, so model cost + other pods + idle = the node bill.
package cost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"

	"sigs.k8s.io/yaml"
)

// UnitPrices are prices per resource per hour, in USD.
type UnitPrices struct {
	CPUCoreHour   float64 `json:"cpuCoreHour"`
	MemoryGiBHour float64 `json:"memoryGiBHour"`
	GPUHour       float64 `json:"gpuHour"`
}

// DefaultUnitPrices are OpenCost's default prices, used to split a node's
// price between CPU, memory and GPU, and as the last-resort node price.
func DefaultUnitPrices() UnitPrices {
	return UnitPrices{CPUCoreHour: 0.031611, MemoryGiBHour: 0.004237, GPUHour: 0.95}
}

// Owned describes hardware you bought: its price spread over its life, plus
// the electricity it draws.
type Owned struct {
	PurchasePrice     float64 `json:"purchasePrice"`     // USD, the whole machine
	LifetimeYears     float64 `json:"lifetimeYears"`     // e.g. 3 or 4
	PowerWatts        float64 `json:"powerWatts"`        // average draw
	ElectricityPerKWh float64 `json:"electricityPerKWh"` // USD
	PUE               float64 `json:"pue"`               // cooling overhead; 1 = none, default 1
}

// Hourly is the machine's cost per hour.
func (o Owned) Hourly() float64 {
	h := 0.0
	if o.LifetimeYears > 0 {
		h = o.PurchasePrice / (o.LifetimeYears * 365 * 24)
	}
	pue := max(o.PUE, 1)
	return h + o.PowerWatts/1000*o.ElectricityPerKWh*pue
}

// NodeRule prices the nodes it matches.
type NodeRule struct {
	// Node name pattern, e.g. "gpu-*" (optional).
	Name string `json:"name,omitempty"`
	// Labels the node must have, all of them (optional).
	Match map[string]string `json:"match,omitempty"`
	// USD per hour. Set this or Owned.
	Hourly float64 `json:"hourly,omitempty"`
	Owned  *Owned  `json:"owned,omitempty"`
	// Shown next to the price, e.g. "EC2 t3.2xlarge shared by 3 kind nodes".
	Note string `json:"note,omitempty"`
}

func (r NodeRule) matches(name string, labels map[string]string) bool {
	if r.Name != "" {
		if ok, _ := path.Match(r.Name, name); !ok {
			return false
		}
	}
	for k, v := range r.Match {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func (r NodeRule) hourly() float64 {
	if r.Owned != nil {
		return r.Owned.Hourly()
	}
	return r.Hourly
}

// ReferenceAPI is a hosted API to compare self-hosting against. You pick
// it and its prices; Sophron doesn't ship prices that go stale.
type ReferenceAPI struct {
	Name        string  `json:"name"`
	InputPer1M  float64 `json:"inputPer1M"`  // USD per million input tokens
	OutputPer1M float64 `json:"outputPer1M"` // USD per million output tokens
}

// PriceFile is the optional pricing configuration (a ConfigMap in the cluster).
type PriceFile struct {
	// Overrides DefaultUnitPrices.
	Defaults *UnitPrices `json:"defaults,omitempty"`
	// First matching rule wins.
	Nodes         []NodeRule     `json:"nodes,omitempty"`
	ReferenceAPIs []ReferenceAPI `json:"referenceAPIs,omitempty"`
}

// LoadPriceFile reads a price file. An empty path means no file.
func LoadPriceFile(p string) (*PriceFile, error) {
	if p == "" {
		return &PriceFile{}, nil
	}
	data, err := os.ReadFile(p) //nolint:gosec // path comes from the operator's flag
	if err != nil {
		return nil, fmt.Errorf("read price file: %w", err)
	}
	f, err := ParsePriceFile(data)
	if err != nil {
		return nil, fmt.Errorf("parse price file %s: %w", p, err)
	}
	return f, f.Validate()
}

// ParsePriceFile parses a price file strictly (unknown keys are errors), but
// accepts prices written as text: `helm --set pricing.nodes[0].hourly=0.42`
// passes "0.42" as a string, because Helm only types whole numbers.
func ParsePriceFile(data []byte) (*PriceFile, error) {
	j, err := yaml.YAMLToJSON(data)
	if err != nil {
		return nil, err
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if j, err = json.Marshal(numbersFromText(v)); err != nil {
		return nil, err
	}
	var f PriceFile
	if err := yaml.UnmarshalStrict(j, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// priceKeys are the price file fields that hold numbers.
var priceKeys = map[string]bool{
	"hourly": true, "cpuCoreHour": true, "memoryGiBHour": true, "gpuHour": true,
	"purchasePrice": true, "lifetimeYears": true, "powerWatts": true, "electricityPerKWh": true, "pue": true,
	"inputPer1M": true, "outputPer1M": true,
}

// numbersFromText turns "0.42" into 0.42 under the numeric keys only, so a
// node label value like "8" under match stays text.
func numbersFromText(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if s, ok := val.(string); ok && priceKeys[k] {
				if _, err := strconv.ParseFloat(s, 64); err == nil {
					x[k] = json.Number(s)
					continue
				}
			}
			x[k] = numbersFromText(val)
		}
	case []any:
		for i := range x {
			x[i] = numbersFromText(x[i])
		}
	}
	return v
}

// Validate checks the file for mistakes that would give silent wrong prices.
func (f *PriceFile) Validate() error {
	var errs []error
	for i, r := range f.Nodes {
		switch {
		case r.Name == "" && len(r.Match) == 0:
			errs = append(errs, fmt.Errorf("nodes[%d]: set name or match, or it prices every node", i))
		case r.Owned == nil && r.Hourly <= 0:
			errs = append(errs, fmt.Errorf("nodes[%d]: set hourly (USD per hour) or owned", i))
		case r.Owned != nil && r.Owned.LifetimeYears <= 0:
			errs = append(errs, fmt.Errorf("nodes[%d]: owned.lifetimeYears must be above 0", i))
		}
		if r.Name != "" {
			if _, err := path.Match(r.Name, ""); err != nil {
				errs = append(errs, fmt.Errorf("nodes[%d]: bad name pattern %q", i, r.Name))
			}
		}
	}
	for i, a := range f.ReferenceAPIs {
		if a.Name == "" || a.InputPer1M < 0 || a.OutputPer1M <= 0 {
			errs = append(errs, fmt.Errorf("referenceAPIs[%d]: needs a name and outputPer1M above 0", i))
		}
	}
	return errors.Join(errs...)
}

func (f *PriceFile) units() UnitPrices {
	if f != nil && f.Defaults != nil {
		return *f.Defaults
	}
	return DefaultUnitPrices()
}

func (f *PriceFile) rule(name string, labels map[string]string) (NodeRule, bool) {
	if f == nil {
		return NodeRule{}, false
	}
	for _, r := range f.Nodes {
		if r.matches(name, labels) {
			return r, true
		}
	}
	return NodeRule{}, false
}
