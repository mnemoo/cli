package upload

import (
	"context"
	"sync"
)

// transferLimiter pipelines PUT requests. sendLimit caps requests that are
// still writing their bodies; outstandingLimit also includes requests whose
// bodies were written and which are waiting for an HTTP response.
type transferLimiter struct {
	mu                sync.Mutex
	sendLimit         int
	outstandingLimit  int
	sending           int
	outstanding       int
	outstandingByFile map[string]int
	waiters           []*transferWaiter
}

type transferWaiter struct {
	file      string
	ready     chan struct{}
	permit    *transferPermit
	granted   bool
	cancelled bool
}

type transferPermit struct {
	limiter  *transferLimiter
	file     string
	bodySent bool
	released bool
}

func newTransferLimiter(sendLimit, outstandingLimit int) *transferLimiter {
	if sendLimit < 1 {
		sendLimit = 1
	}
	if outstandingLimit < sendLimit {
		outstandingLimit = sendLimit
	}
	return &transferLimiter{
		sendLimit:         sendLimit,
		outstandingLimit:  outstandingLimit,
		outstandingByFile: make(map[string]int),
	}
}

func (l *transferLimiter) acquire(ctx context.Context, file string) (*transferPermit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	permit := &transferPermit{limiter: l, file: file}
	waiter := &transferWaiter{file: file, ready: make(chan struct{}), permit: permit}
	l.mu.Lock()
	l.waiters = append(l.waiters, waiter)
	l.grantLocked()
	l.mu.Unlock()

	select {
	case <-waiter.ready:
		return permit, nil
	case <-ctx.Done():
		l.mu.Lock()
		if waiter.granted {
			l.releaseLocked(permit)
		} else {
			waiter.cancelled = true
		}
		l.grantLocked()
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}

// markBodySent releases only the sending slot. The request continues to count
// against outstandingLimit until S3 returns a response.
func (p *transferPermit) markBodySent() {
	if p == nil || p.limiter == nil {
		return
	}
	l := p.limiter
	l.mu.Lock()
	if !p.released && !p.bodySent {
		p.bodySent = true
		l.sending--
		l.grantLocked()
	}
	l.mu.Unlock()
}

// release completes the request permit after an HTTP response or error.
func (p *transferPermit) release() {
	if p == nil || p.limiter == nil {
		return
	}
	l := p.limiter
	l.mu.Lock()
	l.releaseLocked(p)
	l.grantLocked()
	l.mu.Unlock()
}

func (l *transferLimiter) releaseLocked(permit *transferPermit) {
	if permit == nil || permit.released {
		return
	}
	permit.released = true
	if !permit.bodySent {
		l.sending--
	}
	l.outstanding--
	l.outstandingByFile[permit.file]--
	if l.outstandingByFile[permit.file] == 0 {
		delete(l.outstandingByFile, permit.file)
	}
}

func (l *transferLimiter) grantLocked() {
	for l.sending < l.sendLimit && l.outstanding < l.outstandingLimit {
		best := -1
		bestOutstanding := int(^uint(0) >> 1)
		for i, waiter := range l.waiters {
			if waiter.cancelled {
				continue
			}
			fileOutstanding := l.outstandingByFile[waiter.file]
			if fileOutstanding < bestOutstanding {
				best = i
				bestOutstanding = fileOutstanding
			}
		}
		if best == -1 {
			l.compactLocked()
			return
		}

		waiter := l.waiters[best]
		copy(l.waiters[best:], l.waiters[best+1:])
		l.waiters[len(l.waiters)-1] = nil
		l.waiters = l.waiters[:len(l.waiters)-1]

		waiter.granted = true
		l.sending++
		l.outstanding++
		l.outstandingByFile[waiter.file]++
		close(waiter.ready)
	}
	l.compactLocked()
}

func (l *transferLimiter) compactLocked() {
	dst := l.waiters[:0]
	for _, waiter := range l.waiters {
		if waiter != nil && !waiter.cancelled {
			dst = append(dst, waiter)
		}
	}
	for i := len(dst); i < len(l.waiters); i++ {
		l.waiters[i] = nil
	}
	l.waiters = dst
}
