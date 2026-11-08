package actionscache

import "github.com/layercache/layercache/internal/directtransfer"

// SetRemoteDirectBreaker isolates a test client from the process-wide breaker.
func SetRemoteDirectBreaker(storage *RemoteStorage, breaker *directtransfer.Breaker) {
	storage.breaker = breaker
}
