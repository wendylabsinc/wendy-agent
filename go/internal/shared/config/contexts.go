package config

func (c *Config) EnsureContexts() { ensureContexts(c) }
func ensureContexts(c *Config) {
	state := c.identityState()
	state.EnsureContexts()
	c.applyContextState(state)
}
func (c *Config) ContextByName(name string) (*AuthConfig, bool) {
	state := c.identityState()
	selected, ok := state.ContextByName(name)
	return c.hostSession(state, selected), ok
}
