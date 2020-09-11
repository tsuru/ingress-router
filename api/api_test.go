// Copyright 2017 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/tsuru/kubernetes-router/backend"
	"github.com/tsuru/kubernetes-router/router"
	"github.com/tsuru/kubernetes-router/router/mock"
)

type RouterAPISuite struct {
	suite.Suite

	api        *RouterAPI
	mockRouter *mock.RouterMock
	handler    http.Handler
}

func TestRouterAPISuite(t *testing.T) {
	suite.Run(t, &RouterAPISuite{})
}

func (s *RouterAPISuite) SetupTest() {
	s.mockRouter = &mock.RouterMock{}
	s.api = &RouterAPI{
		Backend: &backend.LocalCluster{
			DefaultMode: "mymode",
			Routers: map[string]router.Router{
				"mymode": s.mockRouter,
			},
		},
	}
	s.handler = s.api.Routes()
}

func (s *RouterAPISuite) TestHealthcheckOK() {
	req := httptest.NewRequest("GET", "http://localhost", nil)
	w := httptest.NewRecorder()

	s.api.Healthcheck(w, req)

	resp := w.Result()
	body, _ := ioutil.ReadAll(resp.Body)

	s.Equal(http.StatusOK, resp.StatusCode)
	s.Equal("WORKING", string(body))
}

func (s *RouterAPISuite) TestGetBackend() {
	s.mockRouter.GetAddressesFn = func(id router.InstanceID) ([]string, error) {
		s.Assert().Equal("myapp", id.AppName)
		return []string{"myapp"}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/backend/myapp", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)
	s.True(s.mockRouter.GetAddressesInvoked)
	var data map[string]interface{}
	err := json.Unmarshal(w.Body.Bytes(), &data)
	s.NoError(err)
	s.Equal(map[string]interface{}{
		"address":   "myapp",
		"addresses": []interface{}{"myapp"},
	}, data)
}

func (s *RouterAPISuite) TestGetBackendExplicitMode() {
	mockRouter := &mock.RouterMock{}
	api := RouterAPI{
		Backend: &backend.LocalCluster{
			DefaultMode: "xyz",
			Routers:     map[string]router.Router{"mymode": mockRouter},
		},
	}
	r := api.Routes()
	mockRouter.GetAddressesFn = func(id router.InstanceID) ([]string, error) {
		s.Equal("myapp", id.AppName)
		return []string{"myapp"}, nil
	}
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/mymode/backend/myapp", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)
	s.True(mockRouter.GetAddressesInvoked)
}

func (s *RouterAPISuite) TestGetBackendInvalidMode() {
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/othermode/backend/myapp", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusNotFound, resp.StatusCode)
}

func (s *RouterAPISuite) TestAddBackend() {
	s.mockRouter.CreateFn = func(id router.InstanceID, opts router.Opts) error {
		s.Equal("myapp", id.AppName)
		s.Equal("mypool", opts.Pool)
		s.Equal("443", opts.ExposedPort)
		s.Equal(map[string]string{"custom": "val"}, opts.AdditionalOpts)
		return nil
	}

	reqData, _ := json.Marshal(map[string]string{"tsuru.io/app-pool": "mypool", "exposed-port": "443", "custom": "val"})
	body := bytes.NewReader(reqData)
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/backend/myapp", body)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.CreateInvoked {
		s.Fail("Service Create function not invoked")
	}
}

func (s *RouterAPISuite) TestAddBackendWithHeaderOpts() {
	s.mockRouter.CreateFn = func(id router.InstanceID, opts router.Opts) error {
		s.Equal("myapp", id.AppName)
		s.Equal("mypool", opts.Pool)
		s.Equal("443", opts.ExposedPort)
		s.Equal("a.b", opts.Domain)
		expectedAdditional := map[string]string{"custom": "val", "custom2": "val2"}
		s.Equal(expectedAdditional, opts.AdditionalOpts)
		return nil
	}

	reqData, _ := json.Marshal(map[string]string{"tsuru.io/app-pool": "mypool", "exposed-port": "443", "custom": "val"})
	body := bytes.NewReader(reqData)
	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/backend/myapp", body)
	req.Header.Add("X-Router-Opt", "domain=a.b")
	req.Header.Add("X-Router-Opt", "custom2=val2")
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)
	s.True(s.mockRouter.CreateInvoked)
}

func (s *RouterAPISuite) TestRemoveBackend() {
	s.mockRouter.RemoveFn = func(id router.InstanceID) error {
		s.Equal("myapp", id.AppName)
		return nil
	}

	req := httptest.NewRequest(http.MethodDelete, "http://localhost/api/backend/myapp", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.RemoveInvoked {
		s.Fail("Service Remove function not invoked")
	}
}

func (s *RouterAPISuite) TestAddRoutes() {
	s.mockRouter.UpdateFn = func(id router.InstanceID, extraData router.RoutesRequestExtraData) error {
		s.Equal("myapp", id.AppName)
		return nil
	}
	reqData := router.RoutesRequestData{}
	bodyData, err := json.Marshal(reqData)
	s.NoError(err)

	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/backend/myapp/routes", bytes.NewReader(bodyData))
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)
	s.True(s.mockRouter.UpdateInvoked)
}

func (s *RouterAPISuite) TestSwap() {
	s.mockRouter.SwapFn = func(src, dst router.InstanceID) error {
		s.Equal("myapp", src.AppName)
		s.Equal("otherapp", dst.AppName)
		return nil
	}

	data, _ := json.Marshal(map[string]string{"Target": "otherapp"})
	body := bytes.NewReader(data)

	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/backend/myapp/swap", body)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.SwapInvoked {
		s.Fail("Service Swap function not invoked")
	}
}

func (s *RouterAPISuite) TestInfo() {
	s.mockRouter.SupportedOptionsFn = func() map[string]string {
		return map[string]string{router.ExposedPort: "", router.Domain: "Custom help."}
	}

	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/info", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	var info map[string]string
	err := json.Unmarshal(w.Body.Bytes(), &info)
	s.Require().NoError(err)

	expected := map[string]string{
		"exposed-port": "Port to be exposed by the Load Balancer. Defaults to 80.",
		"domain":       "Custom help.",
	}
	s.Equal(expected, info)
}

func (s *RouterAPISuite) TestGetRoutes() {
	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/backend/myapp/routes", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)
	var data map[string][]string
	err := json.Unmarshal(w.Body.Bytes(), &data)
	s.Require().NoError(err)
	expected := map[string][]string{"addresses": nil}
	s.Equal(expected, data)
}

func (s *RouterAPISuite) TestAddCertificate() {
	certExpected := router.CertData{Certificate: "Certz", Key: "keyz"}

	s.mockRouter.AddCertificateFn = func(id router.InstanceID, certName string, cert router.CertData) error {
		s.Require().Equal(certExpected, cert)
		return nil
	}

	reqData, _ := json.Marshal(certExpected)
	body := bytes.NewReader(reqData)

	req := httptest.NewRequest(http.MethodPut, "http://localhost/api/backend/myapp/certificate/certname", body)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)
	if !s.mockRouter.AddCertificateInvoked {
		s.Fail("Service Addresses function not invoked")
	}
}

func (s *RouterAPISuite) TestGetCertificate() {
	s.mockRouter.GetCertificateFn = func(id router.InstanceID, certName string) (*router.CertData, error) {
		cert := router.CertData{Certificate: "Certz", Key: "keyz"}
		return &cert, nil
	}

	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/backend/myapp/certificate/certname", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.GetCertificateInvoked {
		s.Fail("Service Addresses function not invoked")
	}
	var data router.CertData
	err := json.Unmarshal(w.Body.Bytes(), &data)
	s.Require().NoError(err)

	expected := router.CertData{Certificate: "Certz", Key: "keyz"}
	s.Equal(expected, data)
}

func (s *RouterAPISuite) TestRemoveCertificate() {
	s.mockRouter.RemoveCertificateFn = func(id router.InstanceID, certName string) error {
		return nil
	}

	req := httptest.NewRequest(http.MethodDelete, "http://localhost/api/backend/myapp/certificate/certname", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.RemoveCertificateInvoked {
		s.Fail("Service Addresses function not invoked")
	}
}

func (s *RouterAPISuite) TestSetCname() {
	cnameExpected := "cname1"

	s.mockRouter.SetCnameFn = func(id router.InstanceID, cname string) error {
		s.Equal(cnameExpected, cname)
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "http://localhost/api/backend/myapp/cname/cname1", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.SetCnameInvoked {
		s.Fail("Service Addresses function not invoked")
	}
}

func (s *RouterAPISuite) TestGetCnames() {
	cnames := router.CnamesResp{
		Cnames: []string{
			"cname1",
			"cname2",
		},
	}
	s.mockRouter.GetCnamesFn = func(id router.InstanceID) (*router.CnamesResp, error) {
		return &cnames, nil
	}

	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/backend/myapp/cname", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.GetCnamesInvoked {
		s.Fail("Service Addresses function not invoked")
	}
	var data router.CnamesResp
	err := json.Unmarshal(w.Body.Bytes(), &data)
	s.Require().NoError(err)
	s.Equal(data, cnames)

}

func (s *RouterAPISuite) TestUnsetCname() {
	cnameExpected := "cname1"

	s.mockRouter.UnsetCnameFn = func(id router.InstanceID, cname string) error {
		s.Equal(cnameExpected, cname)
		return nil
	}

	req := httptest.NewRequest(http.MethodDelete, "http://localhost/api/backend/myapp/cname/cname1", nil)
	w := httptest.NewRecorder()

	s.handler.ServeHTTP(w, req)
	resp := w.Result()
	s.Equal(http.StatusOK, resp.StatusCode)

	if !s.mockRouter.UnsetCnameInvoked {
		s.Fail("Service Addresses function not invoked")
	}
}
