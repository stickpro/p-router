package checker

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/stickpro/p-router/internal/config"
	"github.com/stickpro/p-router/internal/repository"
	"github.com/stickpro/p-router/pkg/logger"
	"go.uber.org/zap"
)

type ICheckerService interface {
	Check(ctx context.Context) error
	StartPeriodicCheck(ctx context.Context, interval time.Duration)
}

type Service struct {
	conf   *config.Config
	l      logger.Logger
	repo   repository.IProxyRepository
	client *http.Client
}

func New(conf *config.Config, l logger.Logger, repo repository.IProxyRepository) *Service {
	return &Service{
		conf: conf,
		l:    l,
		repo: repo,
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type CheckResult struct {
	Username string
	Success  bool
	Latency  time.Duration
	Error    error
}

func (s *Service) Check(ctx context.Context) error {
	proxies, err := s.repo.FindAll()
	if err != nil {
		s.l.Error("failed to fetch proxies", err)
		return fmt.Errorf("failed to fetch proxies: %w", err)
	}

	if len(proxies) == 0 {
		s.l.Info("no proxies to check")
		return nil
	}

	s.l.Info("starting proxy check", zap.Int("count", len(proxies)))

	resultChan := make(chan CheckResult, len(proxies))
	var wg sync.WaitGroup

	semaphore := make(chan struct{}, 10)

	for _, proxy := range proxies {
		wg.Add(1)
		go func(p *repository.ProxyModel) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result := s.checkSingleProxy(ctx, p)
			resultChan <- result
		}(proxy)
	}

	go func() {
		wg.Wait()
		close(resultChan)
	}()

	successCount := 0
	failedCount := 0

	for result := range resultChan {
		if result.Success {
			successCount++
			s.l.Infow("proxy check successful",
				"username", result.Username,
				"latency", result.Latency,
			)

			if err := s.repo.ResetFailedChecks(result.Username); err != nil {
				s.l.Errorw("failed to reset failed checks",
					"username", result.Username,
					err,
				)
			}
		} else {
			failedCount++
			s.l.Warnln("proxy check failed",
				"username", result.Username,
				result.Error,
			)

			if err := s.repo.IncrementFailedChecks(result.Username); err != nil {
				s.l.Error("failed to increment failed checks",
					"username", result.Username,
					err,
				)
				continue
			}

			proxy, err := s.repo.FindByUsername(result.Username)
			if err != nil {
				continue
			}

			if proxy == nil {
				continue
			}

			s.l.Warnw("proxy failed checks updated",
				"username", result.Username,
				"failed_checks", proxy.FailedChecks,
			)

			maxFailedChecks := s.conf.Checker.MaxFailedChecks
			if maxFailedChecks == 0 {
				maxFailedChecks = 5
			}

			if proxy.FailedChecks >= maxFailedChecks {
				s.l.Errorw("proxy exceeded max failed checks - deleting",
					"username", result.Username,
					"target", proxy.Target,
					"failed_checks", proxy.FailedChecks,
					"max_allowed", maxFailedChecks,
				)

				if err := s.repo.Delete(result.Username); err != nil {
					s.l.Error("failed to delete proxy",
						zap.String("username", result.Username),
						err,
					)
				} else {
					s.l.Info("proxy deleted successfully",
						zap.String("username", result.Username),
					)
				}
			}
		}
	}

	s.l.Infow("proxy check completed",
		"total", len(proxies),
		"success", successCount,
		"failed", failedCount,
	)

	return nil
}

func (s *Service) checkSingleProxy(ctx context.Context, proxy *repository.ProxyModel) CheckResult {
	result := CheckResult{
		Username: proxy.Username,
		Success:  false,
	}

	start := time.Now()

	if !s.checkTCPConnection(ctx, proxy.Target) {
		result.Error = fmt.Errorf("tcp connection failed")
		result.Latency = time.Since(start)
		return result
	}

	testURL := s.conf.Checker.CheckURL
	if testURL == "" {
		testURL = "http://www.google.com"
	}

	proxyURL, err := url.Parse(fmt.Sprintf("http://%s", proxy.Target))
	if err != nil {
		result.Error = fmt.Errorf("invalid proxy URL: %w", err)
		result.Latency = time.Since(start)
		return result
	}

	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
	if err != nil {
		result.Error = fmt.Errorf("failed to create request: %w", err)
		result.Latency = time.Since(start)
		return result
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_6_6; en-US) AppleWebKit/602.37 (KHTML, like Gecko) Chrome/50.0.2869.109 Safari/602")

	resp, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Errorf("http request failed: %w", err)
		result.Latency = time.Since(start)
		return result
	}
	defer resp.Body.Close()

	result.Latency = time.Since(start)

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		result.Success = true
		return result
	}

	result.Error = fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	return result
}

func (s *Service) checkTCPConnection(ctx context.Context, target string) bool {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
	}

	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (s *Service) StartPeriodicCheck(ctx context.Context, interval time.Duration) {
	s.l.Info("starting periodic proxy check", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := s.Check(ctx); err != nil {
		s.l.Error("initial proxy check failed", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.l.Info("stopping periodic proxy check")
			return
		case <-ticker.C:
			if err := s.Check(ctx); err != nil {
				s.l.Error("periodic proxy check failed", err)
			}
		}
	}
}
