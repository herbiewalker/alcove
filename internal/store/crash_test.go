package store

// simulateCrash stands in for the process dying: the kernel drops the data-directory lock when a
// process exits, so a test that wants "another process opens the data after a crash" releases it.
// The first store is deliberately not closed or sealed.
func (s *Store) simulateCrash() { s.lock.release() }
