package config

import "github.com/nilpntr/content-fabrique/internal/research"

// loadResearch reads the retention of the creator research engine (D-41). A value out of range stops the process from starting: a
// retention that was silently clamped would keep third-party data longer than the operator wrote down.
func (c *Config) loadResearch(getenv Getenv) []error {
	r, err := research.LoadRetention(getenv)
	c.ResearchRetention = r
	if err != nil {
		return []error{err}
	}
	return nil
}
