//go:build !race

package session

// soakCheckBytes is the output soak's size without SOAK=1.
const soakCheckBytes = 20 << 20

// raceSlack widens latency bounds: the race detector slows the parser
// about tenfold, and the session lock is held while it parses a chunk.
const raceSlack = 1

// linkSoakLines is how many unique links TestLinkSoak streams without SOAK=1.
const linkSoakLines = 100_000
