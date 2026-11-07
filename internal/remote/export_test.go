package remote

import "github.com/layercache/layercache/internal/directtransfer"

// SetDirectBreaker isolates a test client from the process-wide breaker.
func SetDirectBreaker(client *TurboClient, breaker *directtransfer.Breaker) {
	client.breaker = breaker
}
