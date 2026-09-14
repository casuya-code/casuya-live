package broker

import "testing"

func TestOutcomeFor(t *testing.T) {
	cases := []struct {
		side string
		h, a float64
		want string
	}{
		{"home", 2, 1, "won"},
		{"home", 1, 2, "lost"},
		{"home", 1, 1, "void"},
		{"away", 1, 2, "won"},
		{"away", 2, 1, "lost"},
		{"away", 1, 1, "void"},
		{"draw", 1, 1, "won"},
		{"draw", 1, 2, "lost"},
		{"cs_2:3", 1, 2, "void"},
	}
	for _, c := range cases {
		if got := outcomeFor(c.side, c.h, c.a); got != c.want {
			t.Fatalf("outcomeFor(%q, %v, %v) = %q, want %q", c.side, c.h, c.a, got, c.want)
		}
	}
}

func TestPnlFor(t *testing.T) {
	if got := pnlFor("won", 25, 8.2); got != 180 {
		t.Fatalf("won pnl = %v, want 180", got)
	}
	if got := pnlFor("lost", 25, 8.2); got != -25 {
		t.Fatalf("lost pnl = %v, want -25", got)
	}
	if got := pnlFor("void", 25, 8.2); got != 0 {
		t.Fatalf("void pnl = %v, want 0", got)
	}
}

func TestSettleOrders(t *testing.T) {
	orders := []SettlementOrder{
		{OrderID: "M1", Market: "2H_1X2", Side: "away", Odds: 8.2, Stake: 25},
		{OrderID: "M2", Market: "2H_1X2", Side: "home", Odds: 3.6, Stake: 25},
		{OrderID: "M3", Market: "2H_1X2", Side: "draw", Odds: 4.2, Stake: 25},
		{OrderID: "M4", Market: "CS_2H", Side: "cs_2:3", Odds: 12, Stake: 25},
	}
	settled := settleOrders(orders, 1, 2)
	want := map[string]float64{"M1": 180, "M2": -25, "M3": -25, "M4": 0}
	if len(settled) != len(orders) {
		t.Fatalf("expected %d settled, got %d", len(orders), len(settled))
	}
	for _, s := range settled {
		if s.Pnl != want[s.OrderID] {
			t.Fatalf("%s pnl = %v, want %v", s.OrderID, s.Pnl, want[s.OrderID])
		}
	}
}

func TestSettlementTotals(t *testing.T) {
	settled := []SettledOrder{
		{SettlementOrder: SettlementOrder{OrderID: "M1"}, Result: "won", Pnl: 180},
		{SettlementOrder: SettlementOrder{OrderID: "M2"}, Result: "won", Pnl: 65},
		{SettlementOrder: SettlementOrder{OrderID: "M3"}, Result: "lost", Pnl: -25},
		{SettlementOrder: SettlementOrder{OrderID: "M4"}, Result: "lost", Pnl: -25},
		{SettlementOrder: SettlementOrder{OrderID: "M5"}, Result: "void", Pnl: 0},
	}
	net, won, lost := settlementTotals(settled)
	if net != 195 {
		t.Fatalf("net = %v, want 195", net)
	}
	if won != 2 {
		t.Fatalf("won = %d, want 2", won)
	}
	if lost != 2 {
		t.Fatalf("lost = %d, want 2", lost)
	}
	if _, _, lost2 := settlementTotals(nil); lost2 != 0 {
		t.Fatalf("empty settlement should stay flat, got lost=%d", lost2)
	}
}