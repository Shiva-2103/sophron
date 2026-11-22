package cost

import "testing"

// helm --set types 0.1109 as a string; the price file must still load.
func TestPricesWrittenAsText(t *testing.T) {
	f, err := ParsePriceFile([]byte(`
nodes:
  - name: "lab-*"
    hourly: "0.1109"
  - match: { node.kubernetes.io/instance-type: "8" }
    owned: { purchasePrice: "3200", lifetimeYears: 3, powerWatts: "450", electricityPerKWh: 0.12 }
referenceAPIs:
  - { name: x, inputPer1M: "0.5", outputPer1M: 1.5 }
`))
	if err != nil {
		t.Fatal(err)
	}
	if f.Nodes[0].Hourly != 0.1109 || f.Nodes[1].Owned.PurchasePrice != 3200 || f.ReferenceAPIs[0].InputPer1M != 0.5 {
		t.Errorf("numbers not read: %+v", f)
	}
	if f.Nodes[1].Match["node.kubernetes.io/instance-type"] != "8" {
		t.Errorf("label value changed: %v", f.Nodes[1].Match)
	}
	if _, err := ParsePriceFile([]byte("nodes:\n  - name: a\n    hourly: cheap\n")); err == nil {
		t.Error("a price that isn't a number must still be an error")
	}
	if _, err := ParsePriceFile([]byte("nodes:\n  - name: a\n    hourlyy: 1\n")); err == nil {
		t.Error("unknown keys must still be errors")
	}
}
