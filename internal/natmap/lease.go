package natmap

import "sync"

type closeOnce struct {
	once sync.Once
	fn   func() error
	err  error
}

func (lease *closeOnce) Close() error {
	if lease == nil {
		return nil
	}
	lease.once.Do(func() {
		if lease.fn != nil {
			lease.err = lease.fn()
		}
	})
	return lease.err
}
