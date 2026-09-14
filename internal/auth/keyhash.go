package auth

// keyHashes returns the hash a new key is stored with and the other hashes the same value may already have
// (OPENLOG_KEY_HASH_SECRET, D-044).
func (s *Service) keyHashes(secret string) (hash []byte, legacy [][]byte) {
	return s.cfg.KeyHasher.Hash(secret), s.cfg.KeyHasher.Legacy(secret)
}
