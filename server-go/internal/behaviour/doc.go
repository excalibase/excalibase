// Package behaviour judges what the network meter measured (EXC-844): which
// apps and which owners behave like a proxy, a VPN node or a relay, judged by
// traffic shape and volume only, never by what an app is called. It produces
// findings with numeric evidence for a human to review. Nothing in this
// package suspends, stops or limits anything.
package behaviour
