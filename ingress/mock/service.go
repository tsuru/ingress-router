// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package mock

type IngressService struct {
	CreateFn      func(string) error
	RemoveFn      func(string) error
	UpdateFn      func(string) error
	SwapFn        func(string, string) error
	GetFn         func(string) (map[string]string, error)
	CreateInvoked bool
	RemoveInvoked bool
	UpdateInvoked bool
	SwapInvoked   bool
	GetInvoked    bool
}

func (s *IngressService) Create(appName string) error {
	s.CreateInvoked = true
	return s.CreateFn(appName)
}

func (s *IngressService) Remove(appName string) error {
	s.RemoveInvoked = true
	return s.RemoveFn(appName)
}

func (s *IngressService) Update(appName string) error {
	s.UpdateInvoked = true
	return s.UpdateFn(appName)
}

func (s *IngressService) Swap(appSrc string, appDst string) error {
	s.SwapInvoked = true
	return s.SwapFn(appSrc, appDst)
}

func (s *IngressService) Get(appName string) (map[string]string, error) {
	s.GetInvoked = true
	return s.GetFn(appName)
}