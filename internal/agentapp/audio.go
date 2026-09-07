package agentapp

import (
	"context"
	"errors"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
	"github.com/yudesk/yudesk/internal/systemaudio"
)

// Called only by the session's serial work queue. Native capture never waits
// for a congested network writer; retain at most 60 ms of the newest samples.
type sessionAudio struct {
	ctx     context.Context
	capture func(context.Context, systemaudio.Sink) error
	send    func(protocol.Message) error
	cancel  context.CancelFunc
	done    chan struct{}
}

func (a *sessionAudio) start() error {
	if a.done != nil {
		select {
		case <-a.done:
			a.done = nil
		default:
			return errors.New("声音尚在运行或停止中，请稍后重试")
		}
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.cancel, a.done = cancel, make(chan struct{})
	done := a.done
	go func() {
		defer close(done)
		defer cancel()
		chunks := make(chan []byte, 3)
		result := make(chan error, 1)
		go func() {
			result <- a.capture(ctx, func(format systemaudio.Format, data []byte) error {
				if format.SampleRate != 48000 || format.Channels != 2 || format.Bits != 16 || len(data)%4 != 0 {
					return errors.New("不支持的系统声音格式")
				}
				for len(data) > 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
					n := min(len(data), 3840)
					chunk := append([]byte(nil), data[:n]...)
					data = data[n:]
					select {
					case chunks <- chunk:
					default:
						select {
						case <-chunks:
						default:
						}
						select {
						case chunks <- chunk:
						default:
						}
					}
				}
				return nil
			})
		}()
		// Wait for native capture to release WASAPI/parec on every exit.
		captureDone := false
		defer func() {
			cancel()
			if !captureDone {
				<-result
			}
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-result:
				captureDone = true
				if ctx.Err() == nil {
					if err == nil {
						err = errors.New("系统声音采集已结束，请重新开启")
					}
					_ = a.send(protocol.Message{Kind: "event", Method: "audio_error", Error: err.Error()})
				}
				return
			case data := <-chunks:
				if ctx.Err() != nil {
					return
				}
				// Use the overall session context for sending: cancelling sound
				// mid-record must not tear down the encrypted desktop connection.
				if a.send(protocol.Message{Kind: "event", Method: "audio", Data: data, Meta: map[string]any{"sampleRate": 48000, "channels": 2, "bits": 16}}) != nil {
					return
				}
			}
		}
	}()
	return nil
}

func (a *sessionAudio) stop() error {
	if a.cancel == nil {
		return nil
	}
	a.cancel()
	select {
	case <-a.done:
		a.done = nil
		a.cancel = nil
		return nil
	case <-a.ctx.Done():
		return a.ctx.Err()
	case <-time.After(2 * time.Second):
		return errors.New("声音停止中，请稍后重试")
	}
}
