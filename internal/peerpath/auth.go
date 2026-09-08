package peerpath

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/yudesk/yudesk/internal/protocol"
)

// Authenticate performs the viewer's synchronous application-auth bootstrap.
// conn must already be E2E authenticated/encrypted. No other reader or writer may
// run until this returns. ctx is the session context; only auth has a 75s limit.
// Errors close conn; success returns the original auth response and final path.
func Authenticate(ctx context.Context, conn net.Conn, params map[string]any, options Options) (final *protocol.Conn, auth protocol.Message, info Info, err error) {
	pc := protocol.NewConn(options.wrap(conn))
	authCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	// A synchronous reader plus closing on cancellation leaves no auth reader
	// behind, even when cancellation interrupts a partially received E2E record.
	stop := context.AfterFunc(authCtx, func() {
		_ = pc.SetDeadline(time.Now())
		_ = pc.Close()
	})
	authDone := false
	defer func() {
		stop()
		if err == nil && !authDone && authCtx.Err() != nil {
			err = authCtx.Err()
		}
		cancel()
		if err != nil {
			if final != nil && final != pc {
				_ = final.Close()
			}
			_ = pc.Close()
			final, info = nil, Info{}
		}
	}()
	deadline, _ := authCtx.Deadline()
	if err = pc.SetDeadline(deadline); err != nil {
		return nil, auth, Info{}, err
	}
	offer := make(map[string]any, len(params)+2)
	for k, v := range params {
		offer[k] = v
	}
	p2p, isBool := params["p2pV1"].(bool)
	p2p = !isBool || p2p
	offer["p2pV1"] = p2p
	offer["interleaveV1"] = true
	pc.OfferInterleaving()
	encoded, err := json.Marshal(offer)
	if err != nil {
		return nil, auth, Info{}, err
	}
	const id = "peerpath-auth"
	if err = pc.WriteMessageContext(authCtx, protocol.Message{Kind: "request", ID: id, Method: "auth", Params: encoded}); err != nil {
		return nil, auth, Info{}, err
	}
	auth, err = pc.ReadMessage()
	if err != nil {
		return nil, auth, Info{}, err
	}
	if auth.Kind != "response" || auth.ID != id {
		return nil, auth, Info{}, fmt.Errorf("%w: invalid auth response", ErrSignaling)
	}
	if !auth.OK {
		return nil, auth, Info{}, fmt.Errorf("peerpath: authentication denied: %s", auth.Error)
	}
	// Fully retire auth cancellation/deadlines before negotiation takes ownership
	// of base. Never touch base deadlines after the tether has been started.
	if !stop() || authCtx.Err() != nil {
		return nil, auth, Info{}, authCtx.Err()
	}
	authDone = true
	cancel()
	if err = pc.SetDeadline(time.Time{}); err != nil {
		return nil, auth, Info{}, err
	}
	interleave, _ := auth.Meta["interleaveV1"].(bool)
	if interleave {
		pc.EnableInterleaving()
	}
	accepted, _ := auth.Meta["p2pV1"].(bool)
	if !p2p || !accepted {
		reason := "peer_unsupported"
		if !p2p {
			reason = "disabled"
		}
		return pc, auth, Info{Mode: "relay", Reason: reason}, nil
	}
	final, info, err = Negotiate(ctx, pc, "viewer", pc.ReadMessage, options)
	if err != nil {
		return nil, auth, Info{}, err
	}
	if interleave {
		final.EnableInterleaving()
	}
	// Ensure a cancellation racing the final handoff also closes direct.
	if ctx.Err() != nil {
		_ = final.Close()
		return nil, auth, Info{}, ctx.Err()
	}
	return final, auth, info, nil
}
