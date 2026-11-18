package main

// A successful network sample includes active connection counters. Process
// snapshots retain observed totals after a connection closes and discard the
// closed connection's baseline. PID + start time isolates each process lifetime.
type processNetworkSample struct {
	sockets map[string][2]uint64
}

func retainProcessNetwork(p *monitoredProcess, old monitoredProcess) {
	sameProcess := p.StartedAt > 0 && p.StartedAt == old.StartedAt
	if p.networkUnavailable {
		if sameProcess {
			p.ReceivedBytes = old.ReceivedBytes
			p.SentBytes = old.SentBytes
			p.networkSockets = old.networkSockets
		}
		return
	}
	if p.networkSample == nil {
		return
	}
	totals := [2]uint64{}
	if sameProcess {
		if old.ReceivedBytes != nil {
			totals[0] = *old.ReceivedBytes
		}
		if old.SentBytes != nil {
			totals[1] = *old.SentBytes
		}
	}
	for connection, current := range p.networkSample.sockets {
		previous, exists := old.networkSockets[connection]
		for direction, count := range current {
			delta := count
			if sameProcess && exists && count >= previous[direction] {
				delta = count - previous[direction]
			}
			totals[direction] += delta
		}
	}
	p.ReceivedBytes = ptr(totals[0])
	p.SentBytes = ptr(totals[1])
	p.networkSockets = p.networkSample.sockets
}

func summarizeProcessThreads(items []monitoredProcess) (int64, int) {
	var total int64
	var unavailable int
	for _, p := range items {
		if p.Threads == nil {
			unavailable++
		} else {
			total += int64(*p.Threads)
		}
	}
	return total, unavailable
}
