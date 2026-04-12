package proxy

import (
	"ai-gateway/logger"
	"fmt"
	"sync"
)

// StaticProvider assigns proxies from a manually configured list
type StaticProvider struct {
	proxies     []ProxyInfo
	assignments map[string]int // accountKey → index in proxies
	nextIdx     int
	tracker     *Tracker
	mu          sync.Mutex
}

type StaticConfig struct {
	Address  string
	Username string
	Password string
	Protocol string
}

func NewStaticProvider(configs []StaticConfig, tracker *Tracker) *StaticProvider {
	proxies := make([]ProxyInfo, 0, len(configs))
	for _, c := range configs {
		protocol := c.Protocol
		if protocol == "" {
			protocol = "socks5"
		}
		proxies = append(proxies, ProxyInfo{
			Address:  c.Address,
			Username: c.Username,
			Password: c.Password,
			Protocol: protocol,
		})
	}
	return &StaticProvider{
		proxies:     proxies,
		assignments: make(map[string]int),
		tracker:     tracker,
	}
}

func (s *StaticProvider) Init() error {
	if len(s.proxies) == 0 {
		return fmt.Errorf("no static proxies configured")
	}
	logger.Info().Int("count", len(s.proxies)).Msg("Static proxies loaded")
	return nil
}

func (s *StaticProvider) GetProxy(accountKey string) (*ProxyInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check existing assignment
	if idx, ok := s.assignments[accountKey]; ok {
		if idx < len(s.proxies) {
			p := s.proxies[idx]
			return &p, nil
		}
	}

	// Assign next proxy round-robin
	if len(s.proxies) == 0 {
		return nil, fmt.Errorf("no static proxies available")
	}

	idx := s.nextIdx % len(s.proxies)
	s.assignments[accountKey] = idx
	s.nextIdx++

	p := s.proxies[idx]
	logger.Info().Str("account", accountKey).Str("address", p.Address).Msg("Assigned static proxy")
	return &p, nil
}

func (s *StaticProvider) ReleaseProxy(accountKey string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.assignments, accountKey)
	return nil
}
