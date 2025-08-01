// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInferencePoolIntegration tests the InferencePool integration with AI Gateway.
func TestInferencePoolIntegration(t *testing.T) {
	// Apply the base test manifest.
	const baseManifest = "../../examples/inference-pool/base.yaml"
	require.NoError(t, kubectlApplyManifest(t.Context(), baseManifest))

	// Test inferencePool with AIGatewayRoute.
	const aiGWRouteManifest = "../../examples/inference-pool/aigwroute.yaml"
	require.NoError(t, kubectlApplyManifest(t.Context(), aiGWRouteManifest))

	egSelector := "gateway.envoyproxy.io/owning-gateway-name=inference-pool-with-aigwroute"
	requireWaitForGatewayPodReady(t, egSelector)

	// Test connectivity to inferencePool + header match + inference pods with valid metrics, should return 200.
	t.Run("endpointpicker_with_aigwroute_matched_header", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", map[string]string{"Authorization": "sk-abcdefghijklmnopqrstuvwxyz"}, http.StatusOK)
	})

	// Test connectivity to inferencePool + header match + inference pods with valid metrics, should return 200.
	t.Run("endpointpicker_with_aigwroute_matched_header", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", map[string]string{"Authorization": "sk-zyxwvutsrqponmlkjihgfedcba"}, http.StatusOK)
	})

	// Test connectivity to inferencePool + unmatched route + inference pods with valid metrics, should return 404 directly.
	t.Run("endpointpicker_with_aigwroute_unmatched", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", nil, http.StatusNotFound)
	})

	// Test connectivity to inferencePool + inference pods with invalid metrics, should fallback to a random pick.
	t.Run("endpointpicker_with_aigwroute_invalid_pod_metrics", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "mistral:latest", nil, http.StatusOK)
	})

	// Test connectivity to aiservicebackend within the same aigatewayroute with inferencePool.
	t.Run("endpointpicker_with_aigwroute_aiservicebackend", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "some-cool-self-hosted-model", nil, http.StatusOK)
	})

	// Test connectivity to inferencePool + inference pods with compressed and uncompressed JSON body.
	t.Run("endpointpicker_with_compressed_json_body", func(t *testing.T) {
		testInferenceGatewayConnectivity(t, egSelector, `{"model":"meta-llama/Llama-3.1-8B-Instruct","messages":[{"role":"user","content":"Say this is a test"}]}`, map[string]string{"Authorization": "sk-abcdefghijklmnopqrstuvwxyz"}, http.StatusOK)
	})

	// Test connectivity to inferencePool + inference pods with compressed and uncompressed JSON body which will be compressed by the EPP.
	t.Run("endpointpicker_with_uncompressed_json_body", func(t *testing.T) {
		testInferenceGatewayConnectivity(t, egSelector, `
{
	"model": "meta-llama/Llama-3.1-8B-Instruct",
	"messages": [{
		"role": "user",
		"content": "Say this is a test"
	}]
}`, map[string]string{"Authorization": "sk-abcdefghijklmnopqrstuvwxyz"}, http.StatusOK)
	})

	t.Cleanup(func() {
		_ = kubectlDeleteManifest(context.Background(), aiGWRouteManifest)
	})

	// Test inferencePool with HTTPRoute.
	const httpRouteManifest = "../../examples/inference-pool/httproute.yaml"
	require.NoError(t, kubectlApplyManifest(t.Context(), httpRouteManifest))

	egSelector = "gateway.envoyproxy.io/owning-gateway-name=inference-pool-with-httproute"
	requireWaitForPodReady(t, egSelector)

	// Test connectivity to inferencePool + inference pods with valid metrics.
	t.Run("endpointpicker_with_httproute_valid_pod_metrics", func(t *testing.T) {
		testInferenceGatewayConnectivityByModel(t, egSelector, "meta-llama/Llama-3.1-8B-Instruct", nil, http.StatusOK)
	})

	t.Cleanup(func() {
		_ = kubectlDeleteManifest(context.Background(), httpRouteManifest)
	})
}

// testInferenceGatewayConnectivityByModel tests that the Gateway is accessible and returns a 200 status code.
// for a valid request to the InferencePool backend for a specific model.
func testInferenceGatewayConnectivityByModel(t *testing.T, egSelector, model string, additionalHeaders map[string]string, expectedStatusCode int) {
	testInferenceGatewayConnectivity(t, egSelector,
		fmt.Sprintf(`{"messages":[{"role":"user","content":"Say this is a test"}],"model":"%s"}`, model), additionalHeaders, expectedStatusCode)
}

// testInferenceGatewayConnectivity tests that the InferenceGateway is working as expected and returns a expected status code.
func testInferenceGatewayConnectivity(t *testing.T, egSelector, body string, additionalHeaders map[string]string, expectedStatusCode int) {
	require.Eventually(t, func() bool {
		fwd := requireNewHTTPPortForwarder(t, egNamespace, egSelector, egDefaultServicePort)
		defer fwd.kill()

		// Set timeout context.
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		// Create a request to the InferencePool backend with the correct model header.
		requestBody := body
		t.Logf("Request body: %s", requestBody)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, fwd.address()+"/v1/chat/completions", strings.NewReader(requestBody))
		require.NoError(t, err)
		// Set required headers for InferencePool routing.
		req.Header.Set("Content-Type", "application/json")
		for key, value := range additionalHeaders {
			req.Header.Set(key, value)
		}

		// Make the request.
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Logf("request failed: %v", err)
			return false
		}
		defer func() { _ = resp.Body.Close() }()

		// Read response body for debugging.
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err, "failed to read response body")
		t.Logf("Response status: %d, body: %s", resp.StatusCode, string(body))

		// Check for successful response (200 OK).
		if resp.StatusCode != expectedStatusCode {
			t.Logf("unexpected status code: %d (expected %d), body: %s", resp.StatusCode, expectedStatusCode, string(body))
			return false
		}

		// Verify we got a valid response body (should contain some content).
		require.NotEmpty(t, body, "response body should not be empty")
		t.Logf("Gateway connectivity test passed: status=%d", resp.StatusCode)
		return true
	}, 2*time.Minute, 5*time.Second, "Gateway should return expected status code", expectedStatusCode)
}
