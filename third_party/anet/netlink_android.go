// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Netlink sockets and messages

package anet

import (
	"syscall"
	"time"
	"unsafe"
)

const (
	netlinkTimeout     = time.Second
	maxNetlinkResponse = 1 << 20
)

// Round the length of a netlink message up to align it properly.
func nlmAlignOf(msglen int) int {
	return (msglen + syscall.NLMSG_ALIGNTO - 1) & ^(syscall.NLMSG_ALIGNTO - 1)
}

// Round the length of a netlink route attribute up to align it
// properly.
func rtaAlignOf(attrlen int) int {
	return (attrlen + syscall.RTA_ALIGNTO - 1) & ^(syscall.RTA_ALIGNTO - 1)
}

// NetlinkRouteRequest represents a request message to receive routing
// and link states from the kernel.
type NetlinkRouteRequest struct {
	Header syscall.NlMsghdr
	Data   syscall.RtGenmsg
}

func (rr *NetlinkRouteRequest) toWireFormat() []byte {
	b := make([]byte, rr.Header.Len)
	*(*uint32)(unsafe.Pointer(&b[0:4][0])) = rr.Header.Len
	*(*uint16)(unsafe.Pointer(&b[4:6][0])) = rr.Header.Type
	*(*uint16)(unsafe.Pointer(&b[6:8][0])) = rr.Header.Flags
	*(*uint32)(unsafe.Pointer(&b[8:12][0])) = rr.Header.Seq
	*(*uint32)(unsafe.Pointer(&b[12:16][0])) = rr.Header.Pid
	b[16] = byte(rr.Data.Family)
	return b
}

func newNetlinkRouteRequest(proto, seq, family int) []byte {
	rr := &NetlinkRouteRequest{}
	rr.Header.Len = uint32(syscall.NLMSG_HDRLEN + syscall.SizeofRtGenmsg)
	rr.Header.Type = uint16(proto)
	rr.Header.Flags = syscall.NLM_F_DUMP | syscall.NLM_F_REQUEST
	rr.Header.Seq = uint32(seq)
	rr.Data.Family = uint8(family)
	return rr.toWireFormat()
}

// NetlinkRIB returns routing information base, as known as RIB, which
// consists of network facility information, states and parameters.
func NetlinkRIB(proto, family int) ([]byte, error) {
	s, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(s)
	sa := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}

	wb := newNetlinkRouteRequest(proto, 1, family)
	if err := syscall.Sendto(s, wb, 0, sa); err != nil {
		return nil, err
	}
	lsa, err := syscall.Getsockname(s)
	if err != nil {
		return nil, err
	}
	lsanl, ok := lsa.(*syscall.SockaddrNetlink)
	if !ok {
		return nil, syscall.EINVAL
	}
	return readNetlinkRIB(lsanl.Pid, netlinkTimeout, func(b []byte, remaining time.Duration) (int, error) {
		return receiveNetlink(s, b, remaining)
	})
}

// A per-receive timeout alone would allow a stream of replies to extend the
// query forever. Recompute its remaining total budget before every receive.
func receiveNetlink(fd int, b []byte, remaining time.Duration) (int, error) {
	if remaining <= 0 {
		return 0, syscall.ETIMEDOUT
	}
	timeout := syscall.NsecToTimeval(remaining.Nanoseconds())
	if err := syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &timeout); err != nil {
		return 0, err
	}
	n, _, err := syscall.Recvfrom(fd, b, syscall.MSG_TRUNC)
	if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
		err = syscall.ETIMEDOUT
	}
	return n, err
}

func readNetlinkRIB(pid uint32, budget time.Duration, receive func([]byte, time.Duration) (int, error)) ([]byte, error) {
	deadline := time.Now().Add(budget)
	var tab []byte
	// Accept larger multipart datagrams without trusting a kernel-supplied size.
	rb := make([]byte, 64<<10)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, syscall.ETIMEDOUT
		}
		nr, err := receive(rb, remaining)
		if err != nil {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, syscall.ETIMEDOUT
		}
		if nr < syscall.NLMSG_HDRLEN {
			return nil, syscall.EINVAL
		}
		if nr > len(rb) || nr > maxNetlinkResponse-len(tab) {
			return nil, syscall.EMSGSIZE
		}
		msgs, err := ParseNetlinkMessage(rb[:nr])
		if err != nil {
			return nil, err
		}
		tab = append(tab, rb[:nr]...)
		for _, m := range msgs {
			if m.Header.Seq != 1 || m.Header.Pid != pid {
				return nil, syscall.EINVAL
			}
			if m.Header.Type == syscall.NLMSG_DONE {
				return tab, nil
			}
			if m.Header.Type == syscall.NLMSG_ERROR {
				return nil, syscall.EINVAL
			}
		}
	}
}

// NetlinkMessage represents a netlink message.
type NetlinkMessage struct {
	Header syscall.NlMsghdr
	Data   []byte
}

// ParseNetlinkMessage parses b as an array of netlink messages and
// returns the slice containing the NetlinkMessage structures.
func ParseNetlinkMessage(b []byte) ([]NetlinkMessage, error) {
	var msgs []NetlinkMessage
	for len(b) >= syscall.NLMSG_HDRLEN {
		h, dbuf, dlen, err := netlinkMessageHeaderAndData(b)
		if err != nil {
			return nil, err
		}
		m := NetlinkMessage{Header: *h, Data: dbuf[:int(h.Len)-syscall.NLMSG_HDRLEN]}
		msgs = append(msgs, m)
		b = b[dlen:]
	}
	if len(b) != 0 {
		return nil, syscall.EINVAL
	}
	return msgs, nil
}

func netlinkMessageHeaderAndData(b []byte) (*syscall.NlMsghdr, []byte, int, error) {
	if len(b) < syscall.NLMSG_HDRLEN {
		return nil, nil, 0, syscall.EINVAL
	}
	h := (*syscall.NlMsghdr)(unsafe.Pointer(&b[0]))
	// Check before conversion/alignment: uint32 -> int can overflow on ARM/386.
	if h.Len < syscall.NLMSG_HDRLEN || uint64(h.Len) > uint64(len(b)) {
		return nil, nil, 0, syscall.EINVAL
	}
	l := nlmAlignOf(int(h.Len))
	if l < int(h.Len) || l > len(b) {
		return nil, nil, 0, syscall.EINVAL
	}
	return h, b[syscall.NLMSG_HDRLEN:], l, nil
}

// NetlinkRouteAttr represents a netlink route attribute.
type NetlinkRouteAttr struct {
	Attr  syscall.RtAttr
	Value []byte
}

// ParseNetlinkRouteAttr parses m's payload as an array of netlink
// route attributes and returns the slice containing the
// NetlinkRouteAttr structures.
func ParseNetlinkRouteAttr(m *NetlinkMessage) ([]NetlinkRouteAttr, error) {
	if m == nil {
		return nil, syscall.EINVAL
	}
	var offset int
	switch m.Header.Type {
	case syscall.RTM_NEWLINK, syscall.RTM_DELLINK:
		offset = syscall.SizeofIfInfomsg
	case syscall.RTM_NEWADDR, syscall.RTM_DELADDR:
		offset = syscall.SizeofIfAddrmsg
	case syscall.RTM_NEWROUTE, syscall.RTM_DELROUTE:
		offset = syscall.SizeofRtMsg
	default:
		return nil, syscall.EINVAL
	}
	if len(m.Data) < offset {
		return nil, syscall.EINVAL
	}
	b := m.Data[offset:]
	var attrs []NetlinkRouteAttr
	for len(b) >= syscall.SizeofRtAttr {
		a, vbuf, alen, err := netlinkRouteAttrAndValue(b)
		if err != nil {
			return nil, err
		}
		ra := NetlinkRouteAttr{Attr: *a, Value: vbuf[:int(a.Len)-syscall.SizeofRtAttr]}
		attrs = append(attrs, ra)
		b = b[alen:]
	}
	if len(b) != 0 {
		return nil, syscall.EINVAL
	}
	return attrs, nil
}

func netlinkRouteAttrAndValue(b []byte) (*syscall.RtAttr, []byte, int, error) {
	if len(b) < syscall.SizeofRtAttr {
		return nil, nil, 0, syscall.EINVAL
	}
	a := (*syscall.RtAttr)(unsafe.Pointer(&b[0]))
	aligned := rtaAlignOf(int(a.Len))
	if int(a.Len) < syscall.SizeofRtAttr || aligned > len(b) {
		return nil, nil, 0, syscall.EINVAL
	}
	return a, b[syscall.SizeofRtAttr:], aligned, nil
}
