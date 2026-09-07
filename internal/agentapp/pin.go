package agentapp

import "crypto/subtle"

func (a *agent) currentPIN() string {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	return a.pin
}

func (a *agent) matchesPIN(pin string) bool {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	return subtle.ConstantTimeCompare([]byte(pin), []byte(a.pin)) == 1
}
