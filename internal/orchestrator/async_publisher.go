package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type asyncPublishRequest struct {
	path    string
	body    any
	barrier chan struct{}
}

type AsyncPublisher struct {
	streamID string
	ch       chan asyncPublishRequest
	done     chan struct{}
	once     sync.Once
	dropped  atomic.Int64
	lastLog  atomic.Int64
	cancel   context.CancelFunc
}

func NewAsyncPublisher(ctx context.Context, c *Client, streamID string) *AsyncPublisher {
	publishCtx, cancel := context.WithCancel(ctx)
	p := &AsyncPublisher{
		streamID: streamID,
		ch:       make(chan asyncPublishRequest, 256),
		done:     make(chan struct{}),
		cancel:   cancel,
	}
	go func() {
		// Do NOT close p.done here: Close() is the single closer (guarded by
		// sync.Once). If this goroutine also closed it on exit, a Close() that
		// fired while the goroutine was mid-publish (publishShort can take up
		// to 5s) would race this deferred close and panic with "close of
		// closed channel", crashing the entire worker process.
		for {
			select {
			case req := <-p.ch:
				if req.barrier != nil {
					close(req.barrier)
					continue
				}
				_ = c.publishShort(publishCtx, req.path, req.body)
			case <-ctx.Done():
				return
			case <-p.done:
				return
			}
		}
	}()
	return p
}

func (p *AsyncPublisher) PublishToken(token string) {
	p.enqueue("/api/internal/streams/"+p.streamID+"/tokens", map[string]string{"token": token})
}

func (p *AsyncPublisher) PublishEvent(eventType, data string) {
	p.enqueue("/api/internal/streams/"+p.streamID+"/events", map[string]string{"type": eventType, "data": data})
}

func (p *AsyncPublisher) Close() {
	p.once.Do(func() { close(p.done); p.cancel() })
}

// Flush is an ordered fence. A following activity must not publish a retry
// reset while tokens from the preceding attempt are still queued.
func (p *AsyncPublisher) Flush(ctx context.Context) error {
	barrier := make(chan struct{})
	select {
	case p.ch <- asyncPublishRequest{barrier: barrier}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return context.Canceled
	}
	select {
	case <-barrier:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return context.Canceled
	}
}

func (p *AsyncPublisher) enqueue(path string, body any) {
	req := asyncPublishRequest{path: path, body: body}
	select {
	case p.ch <- req:
	default:
		dropped := p.dropped.Add(1)
		now := time.Now().Unix()
		last := p.lastLog.Load()
		if dropped == 1 || now-last >= 5 {
			if p.lastLog.CompareAndSwap(last, now) {
				slog.Warn("dropped stream publish (queue full)", "stream_id", p.streamID, "path", path, "dropped_total", dropped)
			}
		}
	}
}
