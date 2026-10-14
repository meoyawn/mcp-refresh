package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type service struct {
	sessionPath string
	http        *http.Client
	log         io.Writer
	mu          sync.Mutex
}

func (s *service) open(ctx context.Context) (*apiClient, error) {
	stored, err := loadSession(s.sessionPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(s.log, "Importing a signed-in ChatGPT browser session…")
		stored, err = importSession(ctx, "", "", "", s.http)
		if err != nil {
			return nil, err
		}
		if err := saveSession(s.sessionPath, stored); err != nil {
			return nil, err
		}
	}
	c, err := newAPIClient(s.http, stored)
	if err != nil {
		return nil, err
	}
	if err := c.authenticate(ctx); err != nil {
		if !isUnauthorized(err) {
			return nil, err
		}
		// Reimport only before an operation; never replay a refresh POST.
		stored, err = importSession(ctx, stored.Browser, stored.Profile, stored.AccountID, s.http)
		if err != nil {
			return nil, err
		}
		if err := saveSession(s.sessionPath, stored); err != nil {
			return nil, err
		}
		c, err = newAPIClient(s.http, stored)
		if err != nil {
			return nil, err
		}
		if err := c.authenticate(ctx); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (s *service) list(ctx context.Context) (links []link, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := s.open(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, saveSession(s.sessionPath, c.session)) }()
	return c.list(ctx)
}

func (s *service) refresh(ctx context.Context, name string) (result refreshResult, err error) {
	if strings.TrimSpace(name) == "" {
		return refreshResult{}, fmt.Errorf("MCP name must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	c, err := s.open(ctx)
	if err != nil {
		return refreshResult{}, err
	}
	defer func() { err = errors.Join(err, saveSession(s.sessionPath, c.session)) }()
	links, err := c.list(ctx)
	if err != nil {
		return refreshResult{}, err
	}
	l, err := resolveLink(links, name)
	if err != nil {
		return refreshResult{}, err
	}
	return c.refresh(ctx, l)
}
