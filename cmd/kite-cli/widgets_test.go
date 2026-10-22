package main

import "testing"

// TestGridClickOnSharedColumn clicks the chain's strike, a column of no
// side, which keeps the selected side.
func TestGridClickOnSharedColumn(t *testing.T) {
	e := newExplorerScreen()
	g := &e.chainGrid
	e.setChainColumns()
	cells := make([]cell, len(g.columns))
	g.setRows([]row{{cells: cells, refs: []any{"call", "put"}}, {cells: cells, refs: []any{"call", "put"}}})
	g.group = 1
	g.view(160, 10, true)

	strike := g.starts[chainStrike] + 1
	hit, _ := g.click(g.x+strike, g.y+g.bandHeight()+1)
	if !hit || g.cursor != 1 || g.group != 1 {
		t.Fatalf("click on the strike: hit %v, row %d, side %d; want row 1 on the put side", hit, g.cursor, g.group)
	}
	hit, _ = g.click(g.x+g.starts[chainCells[0].ltp]+1, g.y+g.bandHeight()+1)
	if !hit || g.group != 0 {
		t.Fatalf("click on the call LTP: hit %v, side %d; want the call side", hit, g.group)
	}
}
