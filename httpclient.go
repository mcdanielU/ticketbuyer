package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"

	"github.com/decred/dcrd/dcrjson/v3"
)

func newHTTPClient(certificateFile string) (*http.Client, error) {

	// Configure tls
	tlsConfig := &tls.Config{
		InsecureSkipVerify: false,
	}

	pem, err := ioutil.ReadFile(certificateFile)
	if err != nil {
		return nil, err
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(pem); !ok {
		return nil, fmt.Errorf("invalid certificate file: %v", certificateFile)
	}
	tlsConfig.RootCAs = pool

	// Create and return the new HTTP client potentially configured with a
	// proxy and TLS.
	var dial func(network, addr string) (net.Conn, error)
	client := http.Client{
		Transport: &http.Transport{
			Dial:            dial,
			TLSClientConfig: tlsConfig,
		},
	}
	return &client, nil
}

func sendPostRequest(jsonRPCServer, rpcUser, rpcPass string, marshalledJSON []byte) (*dcrjson.Response, error) {
	bodyReader := bytes.NewReader(marshalledJSON)
	req, err := http.NewRequest("POST", "https://"+jsonRPCServer, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Close = true
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(rpcUser, rpcPass)

	client, err := newHTTPClient(certificateFile)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	body, _ := ioutil.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode == 200 {
		var resp dcrjson.Response
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, err
		}

		return &resp, nil
	}

	if len(body) == 0 {
		return nil, fmt.Errorf("%d %s", resp.StatusCode,
			http.StatusText(resp.StatusCode))
	}

	return nil, fmt.Errorf("%s", body)
}
