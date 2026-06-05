package router

import (
	"fmt"
	"sync"

	"github.com/stickpro/p-router/internal/repository"
	"github.com/stickpro/p-router/internal/vless"
)

type IProxyROuter interface {
	AddProxy(username, password, target string)
	GetProxy(username, password string) (*ProxyConfig, bool)
	RemoveProxy(username string)
	ListProxies() map[string]string
}

type ProxyConfig struct {
	ID         int64
	Username   string
	Password   string
	Target     string // raw target stored in DB
	Protocol   string // "http" | "socks5" | "vless"
	TargetAddr string // host:port of the upstream proxy (empty for vless)
	ProxyUser  string // upstream proxy auth (SOCKS5 only)
	ProxyPass  string // upstream proxy auth (SOCKS5 only)
	VLESS      *vless.Config // non-nil when Protocol == "vless"
}

func configFromModel(model *repository.ProxyModel) *ProxyConfig {
	pt := model.ParseTarget()
	cfg := &ProxyConfig{
		ID:         model.ID,
		Username:   model.Username,
		Password:   model.Password,
		Target:     model.Target,
		Protocol:   pt.Protocol,
		TargetAddr: pt.Addr,
		ProxyUser:  pt.ProxyUser,
		ProxyPass:  pt.ProxyPass,
	}
	if pt.Protocol == "vless" {
		vcfg, err := vless.ParseConfig(model.Target)
		if err == nil {
			cfg.VLESS = vcfg
		}
	}
	return cfg
}

type ProxyRouter struct {
	repo  repository.IProxyRepository
	cache map[string]*ProxyConfig
	mu    sync.RWMutex
}

func NewProxyRouter(repo repository.IProxyRepository) *ProxyRouter {
	pr := &ProxyRouter{
		repo:  repo,
		cache: make(map[string]*ProxyConfig),
	}

	pr.loadCache()

	return pr
}

func (pr *ProxyRouter) loadCache() error {
	models, err := pr.repo.FindAll()
	if err != nil {
		return err
	}

	pr.mu.Lock()
	defer pr.mu.Unlock()

	for _, model := range models {
		pr.cache[model.Username] = configFromModel(model)
	}

	return nil
}

func (pr *ProxyRouter) AddProxy(username, password, target string) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.cache[username]; exists {
		return fmt.Errorf("proxy with username %s already exists", username)
	}

	model, err := pr.repo.Create(username, password, target)
	if err != nil {
		return err
	}

	pr.cache[username] = configFromModel(model)

	return nil
}

func (pr *ProxyRouter) UpdateProxy(username, password, target string) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	config, exists := pr.cache[username]
	if !exists {
		return fmt.Errorf("proxy with username %s not found", username)
	}

	if err := pr.repo.Update(username, password, target); err != nil {
		return err
	}

	model := &repository.ProxyModel{
		ID:       config.ID,
		Username: username,
		Password: password,
		Target:   target,
	}
	updated := configFromModel(model)
	*config = *updated

	return nil
}

func (pr *ProxyRouter) GetProxy(username, password string) (*ProxyConfig, bool) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	config, exists := pr.cache[username]
	if !exists || config.Password != password {
		return nil, false
	}
	return config, true
}

func (pr *ProxyRouter) RemoveProxy(username string) error {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	if _, exists := pr.cache[username]; !exists {
		return fmt.Errorf("proxy with username %s not found", username)
	}

	if err := pr.repo.Delete(username); err != nil {
		return err
	}

	delete(pr.cache, username)
	return nil
}

func (pr *ProxyRouter) ListProxies() (map[string]string, error) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	result := make(map[string]string)
	for username, config := range pr.cache {
		result[username] = config.Target
	}
	return result, nil
}

func (pr *ProxyRouter) GetAllProxies() ([]*ProxyConfig, error) {
	pr.mu.RLock()
	defer pr.mu.RUnlock()

	result := make([]*ProxyConfig, 0, len(pr.cache))
	for _, config := range pr.cache {
		cfg := *config
		result = append(result, &cfg)
	}
	return result, nil
}
