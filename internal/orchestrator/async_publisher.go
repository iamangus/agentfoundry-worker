package orchestrator

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type asyncPublishRequest struct {
	path string
	body any
}

type AsyncPublisher struct {
	streamID string
	ch       chan asyncPublishRequest
	done     chan struct{}
	once     sync.Once
	dropped  atomic.Int64
	lastLog  atomic.Int64
}

func NewAsyncPublisher(ctx context.Context, c *Client, streamID string) *AsyncPublisher {
	p := &AsyncPublisher{
		streamID: streamID,
		ch:       make(chan asyncPublishRequest, 256),
		done:     make(chan struct{}),
	}
	go func() {
		defer close(p.done)
		for {
			select {
			case req := <-p.ch:
				_ = c.publishShort(context.Background(), req.path, req.body)
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
	p.once.Do(func() { close(p.done) })
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
