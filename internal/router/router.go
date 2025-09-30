package router

import (
	"fmt"
	"sync"

	"github.com/stickpro/p-router/internal/repository"
)

type IProxyROuter interface {
	AddProxy(username, password, target string)
	GetProxy(username, password string) (*ProxyConfig, bool)
	RemoveProxy(username string)
	ListProxies() map[string]string
}

type ProxyConfig struct {
	ID       int64
	Username string
	Password string
	Target   string
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
		pr.cache[model.Username] = &ProxyConfig{
			ID:       model.ID,
			Username: model.Username,
			Password: model.Password,
			Target:   model.Target,
		}
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

	pr.cache[username] = &ProxyConfig{
		ID:       model.ID,
		Username: model.Username,
		Password: model.Password,
		Target:   model.Target,
	}

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

	config.Password = password
	config.Target = target

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
		result = append(result, &ProxyConfig{
			ID:       config.ID,
			Username: config.Username,
			Password: config.Password,
			Target:   config.Target,
		})
	}
	return result, nil
}
