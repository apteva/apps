package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

func (p *pool) admissionLimitLocked(class string) (int, int) {
	s := p.settingsLocked()
	memory, workers := s.TotalMemoryMB, s.MaxWorkers
	// Reserved partitions cannot be borrowed by roots/background jobs.
	if class == "nested" {
		memory = s.NestedMemoryMB
		workers = s.NestedWorkers
	} else {
		memory -= s.NestedMemoryMB
		workers -= s.NestedWorkers
		if class == "background" || class == "preparation" {
			memory -= s.InteractiveMemoryMB
			workers -= s.InteractiveWorkers
		}
	}
	return memory, workers
}
func (p *pool) classUsageLocked(class string) (int, int) {
	memory, count := 0, 0
	for c, u := range p.classReservations {
		include := c != "nested"
		if class == "nested" {
			include = c == "nested"
		} else if class == "background" || class == "preparation" {
			include = c == "background" || c == "preparation"
		}
		if include {
			memory += u[0]
			count += u[1]
		}
	}
	return memory, count
}
func (p *pool) reserveWorker(ctx context.Context, fn *Function) (string, error) {
	class := requestClass(ctx, fn)
	memory := clampInt(fn.MaxMemoryMB, defaultMemoryMB, 16, maxMemoryMB)
	wait, cancel := capacityWaitContext(ctx, fn)
	defer cancel()
	started := time.Now()
	defer func() { traceFrom(ctx).addCapacityWait(time.Since(started)) }()
	var reason *ResourceError
	for {
		if err := ctx.Err(); err != nil {
			return class, err
		}
		p.mu.Lock()
		if p.closed || p.deleted[fn.InstanceKey] {
			p.mu.Unlock()
			return class, resourceError("runtime_stopped", "worker pool closed")
		}
		if p.classReservations == nil {
			p.classReservations = map[string][2]int{}
		}
		_, max := p.admissionLimitLocked(class)
		_, count := p.classUsageLocked(class)
		s := p.settingsLocked()
		reason = p.memoryAdmissionErrorLocked(class, memory, p.admissionMemoryLocked())
		if reason == nil && (len(p.globalSem) >= s.MaxWorkers || len(p.globalSem) >= cap(p.globalSem) || count >= max) {
			reason = resourceError("worker_limit", "worker slots occupied")
		}
		if class == "preparation" && reason == nil && p.classReservations["preparation"][1] >= s.PreparationWorkers {
			reason = resourceError("preparation_limit", "background warm-worker budget occupied")
		}
		if reason == nil {
			p.globalSem <- struct{}{}
			p.liveMB += memory
			u := p.classReservations[class]
			p.classReservations[class] = [2]int{u[0] + memory, u[1] + 1}
			p.mu.Unlock()
			return class, nil
		}
		p.mu.Unlock()
		if p.evictIdleClass(class) {
			continue
		}
		// Child requests never wait behind their own parent: finite reserve or a
		// distinguishable error, including fanout beyond that reserve.
		if class == "nested" {
			reason.Code = "nested_capacity_exhausted"
			return class, p.reject(reason)
		}
		if !reason.Retryable || class == "preparation" {
			return class, p.reject(reason)
		}
		select {
		case <-ctx.Done():
			return class, ctx.Err()
		case <-wait.Done():
			if err := ctx.Err(); err != nil {
				return class, err
			}
			reason.QueueTimedOut = true
			reason.Reason += "; capacity wait deadline expired"
			return class, p.reject(reason)
		case <-p.stop:
			return class, resourceError("runtime_stopped", "pool stopped")
		case <-p.wake:
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (p *pool) releaseReservation(class string, memory int) {
	p.mu.Lock()
	p.liveMB -= memory
	u := p.classReservations[class]
	p.classReservations[class] = [2]int{u[0] - memory, u[1] - 1}
	p.mu.Unlock()
	<-p.globalSem
	p.signal()
}
func (p *pool) evictIdleClass(class string) bool {
	p.mu.Lock()
	var victim *worker
	var owner *fnPool
	for _, fp := range p.byFn {
		n := len(fp.idle)
		for i := 0; i < n; i++ {
			w := <-fp.idle
			eligible := class == "interactive" || class == "nested" || w.capacityClass == "background" || w.capacityClass == "preparation"
			if victim == nil && eligible {
				victim = w
				owner = fp
			} else {
				fp.idle <- w
			}
		}
		if victim != nil {
			break
		}
	}
	_ = owner
	p.mu.Unlock()
	if victim != nil {
		p.discard(victim)
		return true
	}
	return false
}
func (p *pool) admitInvocation(ctx context.Context, fn *Function, fp *fnPool) (finish func(), ready func(), err error) {
	started := time.Now()
	defer func() { traceFrom(ctx).addCapacityWait(time.Since(started)) }()
	p.mu.Lock()
	s := p.settingsLocked()
	class := requestClass(ctx, fn)
	if p.queueClasses == nil {
		p.queueClasses = map[string]int{}
	}
	limit, used := s.MaxQueue-s.NestedQueue, 0
	for c, n := range p.queueClasses {
		if class == "nested" && c == "nested" || class != "nested" && c != "nested" {
			if class != "background" || c == "background" {
				used += n
			}
		}
	}
	if class == "nested" {
		limit = s.NestedQueue
	} else if class == "background" {
		limit -= s.InteractiveQueue
	}
	var admissionErr error
	switch {
	case used >= limit:
		admissionErr = resourceError("queue_limit", "class waiting queue is full")
	case len(p.globalQueue) >= s.MaxQueue || len(p.globalQueue) >= cap(p.globalQueue):
		admissionErr = resourceError("queue_limit", "global waiting queue is full")
	case len(fp.queue) >= s.MaxQueuePerFunction || len(fp.queue) >= cap(fp.queue):
		admissionErr = resourceError("function_queue_limit", "function waiting queue is full")
	default:
		p.globalQueue <- struct{}{}
		fp.queue <- struct{}{}
		p.queueClasses[class]++
	}
	p.mu.Unlock()
	if admissionErr != nil {
		return nil, nil, p.reject(admissionErr)
	}

	var once sync.Once
	ready = func() {
		once.Do(func() {
			p.mu.Lock()
			p.queueClasses[class]--
			<-fp.queue
			<-p.globalQueue
			p.mu.Unlock()
			p.signal()
		})
	}
	queueRelease := ready
	defer func() {
		if err != nil {
			queueRelease()
		}
	}()
	if t := traceFrom(ctx); t != nil {
		t.state("queued")
	}
	wait, cancel := capacityWaitContext(ctx, fn)
	defer cancel()
	for {
		p.mu.Lock()
		if len(fp.sem) < policy(fn).Concurrency && len(fp.sem) < cap(fp.sem) {
			fp.sem <- struct{}{}
			p.mu.Unlock()
			return func() { ready(); <-fp.sem; p.signal() }, ready, nil
		}
		p.mu.Unlock()
		if requestClass(ctx, fn) == "nested" {
			return nil, nil, p.reject(resourceError("nested_capacity_exhausted", "nested function concurrency limit reached"))
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-wait.Done():
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			reason := resourceError("function_worker_limit", fmt.Sprintf("function concurrency %d remained occupied until queue deadline", policy(fn).Concurrency))
			reason.QueueTimedOut = true
			return nil, nil, p.reject(reason)
		case <-p.stop:
			return nil, nil, resourceError("runtime_stopped", "pool stopped")
		case <-p.wake:
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (p *pool) acquireDownstream(ctx context.Context, class string) (func(), error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		s := p.settingsLocked()
		limit := s.MaxDownstream
		used := 0
		for c, n := range p.downstreamClasses {
			if class == "nested" && c == "nested" || class != "nested" && c != "nested" {
				if class != "background" || c == "background" {
					used += n
				}
			}
		}
		if class == "nested" {
			limit = s.NestedDownstream
		} else {
			limit -= s.NestedDownstream
			if class == "background" {
				limit -= s.InteractiveDownstream
			}
		}
		if used < limit && len(p.downstream) < s.MaxDownstream && len(p.downstream) < cap(p.downstream) {
			p.downstream <- struct{}{}
			if p.downstreamClasses == nil {
				p.downstreamClasses = map[string]int{}
			}
			p.downstreamClasses[class]++
			p.mu.Unlock()
			return func() { p.mu.Lock(); p.downstreamClasses[class]--; p.mu.Unlock(); <-p.downstream; p.signal() }, nil
		}
		p.mu.Unlock()
		if class == "nested" {
			return nil, p.reject(resourceError("nested_downstream_limit", "nested downstream reserve exhausted"))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		case <-p.stop:
			return nil, resourceError("runtime_stopped", "pool stopped")
		}
	}
}

func (p *pool) reclassify(w *worker, class string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if w.capacityClass == class {
		return true
	}
	old := w.capacityClass
	m := p.admissionMemoryLocked()
	charge := m.workerCharges[w]
	m.ByClass[old] -= charge
	u := p.classReservations[old]
	p.classReservations[old] = [2]int{u[0] - w.memoryMB, u[1] - 1}
	limit, max := p.admissionLimitLocked(class)
	_, count := p.classUsageLocked(class)
	if m.classUsage(class)+charge > limit || count+1 > max {
		p.classReservations[old] = u
		return false
	}
	v := p.classReservations[class]
	p.classReservations[class] = [2]int{v[0] + w.memoryMB, v[1] + 1}
	w.capacityClass = class
	return true
}

type queueDeadlineKey struct{}

func capacityWaitContext(ctx context.Context, fn *Function) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(time.Duration(policy(fn).QueueMS) * time.Millisecond)
	if d, ok := ctx.Value(queueDeadlineKey{}).(time.Time); ok && d.Before(deadline) {
		deadline = d
	}
	return context.WithDeadline(ctx, deadline)
}

// Downstream buffers share the hard protocol ceiling but background calls
// cannot reserve the portions needed by interactive and nested calls. The
// nested portion is protected capacity, not a ceiling: children may borrow any
// unused shared bytes, always subject to the same global atomic byte limit.
func (p *pool) acquireProtocol(ctx context.Context, class string, n int64) (func(), error) {
	wait, cancel := context.WithTimeout(ctx, p.protocolConfig().wait)
	defer cancel()
	queued := false
	lastReason, lastMessage := "protocol_memory_limit", "protocol buffer ceiling exhausted"
	defer func() {
		if queued {
			p.protocolWaiters.Add(-1)
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return nil, err
		}
		if class != "nested" && wait.Err() != nil {
			p.mu.Unlock()
			return nil, p.reject(resourceError(lastReason, lastMessage+"; protocol wait deadline expired"))
		}
		b := p.protocolConfig()
		allowed := protocolClassLimit(b.hard, class)
		if n > allowed {
			p.mu.Unlock()
			return nil, p.reject(resourceError("protocol_memory_limit", "response allowance exceeds the class protocol ceiling"))
		}
		if p.closed {
			p.mu.Unlock()
			return nil, resourceError("runtime_stopped", "pool closed")
		}
		if p.protocolClasses == nil {
			p.protocolClasses = map[string]int64{}
		}
		var used int64
		for c, v := range p.protocolClasses {
			if class == "nested" && c == "nested" || class != "nested" && c != "nested" {
				if class != "background" || c == "background" {
					used += v
				}
			}
		}
		reason := "protocol_memory_limit"
		if used+n <= allowed {
			reason = reserveProtocolBudget(b, n, used+n > protocolClassLimit(b.target, class))
		}
		if reason == "" {
			p.protocolClasses[class] += n
			p.mu.Unlock()
			return func() { p.mu.Lock(); p.protocolClasses[class] -= n; p.mu.Unlock(); protocolBytes.Add(-n); p.signal() }, nil
		}
		p.mu.Unlock()
		message := "protocol buffer ceiling exhausted"
		if reason == "protocol_host_memory_pressure" {
			message = "protocol burst denied: insufficient or unavailable host memory headroom"
		}
		// A waiting parent may hold exactly the bytes its child needs. Children
		// borrow immediately when safe; they never queue behind that parent.
		if class == "nested" {
			return nil, p.reject(resourceError(reason, message+" during nested call"))
		}
		lastReason, lastMessage = reason, message
		if !queued {
			p.protocolWaiters.Add(1)
			queued = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wait.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, p.reject(resourceError(reason, message+"; protocol wait deadline expired"))
		case <-p.stop:
			return nil, resourceError("runtime_stopped", "pool stopped")
		case <-p.wake:
		case <-time.After(5 * time.Millisecond):
		}
	}
}
