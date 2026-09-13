# YuDesk FRP patch record

This directory is a source snapshot of
[`YUCONGGEN/frp`](https://github.com/YUCONGGEN/frp) commit
`8666e3643f4e8cc3ec65780c48e20c8904b17856` (FRP 0.70.1).

YuDesk applies one local stability patch to `client/service.go`: accesses to the
active controller during startup, reconnect, configuration update, and shutdown
are synchronized with `ctlMu`. The patch removes a data race that can otherwise
terminate or destabilize an embedded FRP client while the YuDesk process exits.

The upstream Apache-2.0 license is retained in `LICENSE`.
