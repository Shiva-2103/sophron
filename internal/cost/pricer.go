package cost

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Shiva-2103/sophron/internal/collector"
	"github.com/Shiva-2103/sophron/internal/kube"
	"github.com/Shiva-2103/sophron/internal/prom"
)

// Where a node's price came from, best first.
const (
	SourceNodeLabel = "node label"
	SourcePriceFile = "price file"
	SourceOpenCost  = "opencost"
	SourceDefault   = "default prices" // an estimate
)

const gib = 1 << 30

// NodeSource is what pricing needs from Kubernetes.
type NodeSource interface {
	Node(name string) (kube.NodeInfo, bool)
	Nodes() []kube.NodeInfo
	NodeRequests(name string) kube.Requests
}

// NodePrice is one node's hourly price and its split per resource.
type NodePrice struct {
	Node   string  `json:"node"`
	Hourly float64 `json:"hourly"`
	Source string  `json:"source"`
	Note   string  `json:"note,omitempty"`
	// True when no real price was found and defaults were used.
	Estimate bool       `json:"estimate"`
	Units    UnitPrices `json:"units"`
}

// Pricer prices nodes and the model pods on them.
type Pricer struct {
	Nodes NodeSource
	Prom  prom.Querier // optional, for OpenCost's node prices
	File  *PriceFile
	Log   *slog.Logger

	mu         sync.Mutex
	openCost   map[string]float64
	openCostAt time.Time
}

// openCostEvery limits how often OpenCost's prices are re-read.
const openCostEvery = 5 * time.Minute

// NodePrice works out one node's price.
func (p *Pricer) NodePrice(ctx context.Context, n kube.NodeInfo) NodePrice {
	np := NodePrice{Node: n.Name}
	switch {
	case parsePrice(n.HourlyPrice) > 0:
		np.Hourly, np.Source = parsePrice(n.HourlyPrice), SourceNodeLabel
	default:
		if r, ok := p.File.rule(n.Name, n.Labels); ok {
			np.Hourly, np.Source, np.Note = r.hourly(), SourcePriceFile, r.Note
		} else if v := p.openCostPrice(ctx, n.Name); v > 0 {
			np.Hourly, np.Source = v, SourceOpenCost
		}
	}
	units := p.File.units()
	list := n.CPUCores*units.CPUCoreHour + n.MemoryBytes/gib*units.MemoryGiBHour + float64(n.GPUs)*units.GPUHour
	if np.Hourly <= 0 {
		// No price anywhere: the node costs what default unit prices say.
		np.Hourly, np.Source, np.Estimate = list, SourceDefault, true
	}
	// Split the node price in the same proportions as the default unit
	// prices, so a GPU node's price lands mostly on its GPUs.
	if list > 0 {
		scale := np.Hourly / list
		np.Units = UnitPrices{
			CPUCoreHour:   units.CPUCoreHour * scale,
			MemoryGiBHour: units.MemoryGiBHour * scale,
			GPUHour:       units.GPUHour * scale,
		}
	}
	return np
}

func parsePrice(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(s, "$")), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// openCostPrice reads OpenCost's node_total_hourly_cost metric, if OpenCost
// is installed. Missing OpenCost is normal and not an error.
func (p *Pricer) openCostPrice(ctx context.Context, node string) float64 {
	if p.Prom == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.openCost == nil || time.Since(p.openCostAt) > openCostEvery {
		p.openCost, p.openCostAt = map[string]float64{}, time.Now()
		res, err := p.Prom.Query(ctx, "max by (node) (node_total_hourly_cost)", time.Now())
		if err != nil && p.Log != nil {
			p.Log.Debug("no OpenCost prices", "error", err)
		}
		for _, r := range res {
			if n := r.Labels["node"]; n != "" && r.Value > 0 {
				p.openCost[n] = r.Value
			}
		}
	}
	return p.openCost[node]
}

// PodCost is what one pod costs per hour on a node with this price: the
// larger of reserved and used, per resource. GPUs are charged whole.
func PodCost(s collector.Snapshot, np NodePrice) float64 {
	cpu := s.CPURequestCores
	if s.CPUCores != nil {
		cpu = max(cpu, *s.CPUCores)
	}
	mem := s.MemoryRequestBytes
	if s.MemoryWorkingSetBytes != nil {
		mem = max(mem, *s.MemoryWorkingSetBytes)
	}
	c := cpu*np.Units.CPUCoreHour + mem/gib*np.Units.MemoryGiBHour + float64(s.GPURequest)*np.Units.GPUHour
	return min(c, np.Hourly)
}

// RequestCost is what reservations cost on a node (used for non-model pods).
func RequestCost(r kube.Requests, np NodePrice) float64 {
	return r.CPUCores*np.Units.CPUCoreHour + r.MemoryBytes/gib*np.Units.MemoryGiBHour + float64(r.GPUs)*np.Units.GPUHour
}

// Price fills in the cost of every snapshot that has a node. It implements
// collector.Pricer. A pod without a node (Pending) gets no cost: nothing is
// billed for it yet.
func (p *Pricer) Price(ctx context.Context, snaps []collector.Snapshot) {
	prices := map[string]NodePrice{}
	for i := range snaps {
		s := &snaps[i]
		if s.Node == "" {
			continue
		}
		np, ok := prices[s.Node]
		if !ok {
			n, found := p.Nodes.Node(s.Node)
			if !found {
				continue
			}
			np = p.NodePrice(ctx, n)
			prices[s.Node] = np
		}
		c, nc := PodCost(*s, np), np.Hourly
		s.CostPerHour, s.NodeCostPerHour, s.PriceSource = &c, &nc, np.Source
	}
}
