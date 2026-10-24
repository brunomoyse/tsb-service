package hubrise_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"tsb-service/internal/modules/hubrise"
	hubtest "tsb-service/internal/modules/hubrise/testhelpers"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func validEnv() map[string]string {
	return map[string]string{
		"HUBRISE_CLIENT_ID":     "client-id",
		"HUBRISE_CLIENT_SECRET": "client-secret",
		"HUBRISE_TOKEN_KEY":     base64.StdEncoding.EncodeToString(hubtest.TestTokenKey),
	}
}

func TestLoadConfigUnsetIsStandalone(t *testing.T) {
	cfg, err := hubrise.LoadConfig(env(nil))
	require.NoError(t, err)
	assert.False(t, cfg.Available())
	assert.Equal(t, []string{"HUBRISE_CLIENT_ID", "HUBRISE_CLIENT_SECRET", "HUBRISE_TOKEN_KEY"}, cfg.Missing())
	assert.Equal(t, hubrise.DefaultAPIBaseURL, cfg.APIBaseURL)
	assert.Equal(t, hubrise.DefaultOAuthBaseURL, cfg.OAuthBaseURL)
}

func TestLoadConfigComplete(t *testing.T) {
	m := validEnv()
	m["HUBRISE_CLIENT_ID"] = "  client-id \n"
	cfg, err := hubrise.LoadConfig(env(m))
	require.NoError(t, err)
	assert.True(t, cfg.Available())
	assert.Empty(t, cfg.Missing())
	assert.Equal(t, "client-id", cfg.ClientID, "values are trimmed")
	assert.Equal(t, "client-secret", cfg.ClientSecret.Reveal())
	assert.Equal(t, hubtest.TestTokenKey, cfg.TokenKey)
}

func TestLoadConfigPartialIsUnavailable(t *testing.T) {
	for _, missing := range []string{"HUBRISE_CLIENT_ID", "HUBRISE_CLIENT_SECRET", "HUBRISE_TOKEN_KEY"} {
		m := validEnv()
		delete(m, missing)
		cfg, err := hubrise.LoadConfig(env(m))
		require.NoError(t, err)
		assert.False(t, cfg.Available(), "without %s", missing)
		assert.Equal(t, []string{missing}, cfg.Missing())
	}
}

func TestLoadConfigBaseURLs(t *testing.T) {
	m := validEnv()
	m["HUBRISE_API_BASE_URL"] = "http://127.0.0.1:9000/v1/"
	m["HUBRISE_OAUTH_BASE_URL"] = " https://mock.example/oauth2/v1 "
	cfg, err := hubrise.LoadConfig(env(m))
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:9000/v1", cfg.APIBaseURL, "no trailing slash")
	assert.Equal(t, "https://mock.example/oauth2/v1", cfg.OAuthBaseURL)

	for _, bad := range []string{"not a url", "ftp://host/v1", "/relative", "https://"} {
		for _, name := range []string{"HUBRISE_API_BASE_URL", "HUBRISE_OAUTH_BASE_URL"} {
			m := validEnv()
			m[name] = bad
			_, err := hubrise.LoadConfig(env(m))
			require.Error(t, err, "%s=%q", name, bad)
			assert.Contains(t, err.Error(), name)
		}
	}
}

func TestLoadConfigReplicas(t *testing.T) {
	cfg, err := hubrise.LoadConfig(env(validEnv()))
	require.NoError(t, err)
	assert.Equal(t, hubrise.DefaultReplicas, cfg.Replicas, "production runs two replicas")

	m := validEnv()
	m["HUBRISE_REPLICAS"] = " 1 "
	cfg, err = hubrise.LoadConfig(env(m))
	require.NoError(t, err)
	assert.Equal(t, 1, cfg.Replicas)

	for _, bad := range []string{"0", "-1", "two", "1.5", "101"} {
		m["HUBRISE_REPLICAS"] = bad
		_, err := hubrise.LoadConfig(env(m))
		require.Error(t, err, bad)
		assert.Contains(t, err.Error(), "HUBRISE_REPLICAS")
	}
}

func TestLoadConfigBadKeyIsLoud(t *testing.T) {
	for name, key := range map[string]string{
		"not base64": "%%%not-base64%%%",
		"too short":  base64.StdEncoding.EncodeToString([]byte("short")),
	} {
		m := validEnv()
		m["HUBRISE_TOKEN_KEY"] = key
		_, err := hubrise.LoadConfig(env(m))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), "HUBRISE_TOKEN_KEY")
		assert.NotContains(t, err.Error(), key, "the error must not echo the key")
	}
	// Even with the other variables unset: a typo must not silently pass for standalone mode.
	_, err := hubrise.LoadConfig(env(map[string]string{"HUBRISE_TOKEN_KEY": "oops"}))
	assert.Error(t, err)
}

func TestConfigNeverPrintsSecrets(t *testing.T) {
	cfg, err := hubrise.LoadConfig(env(validEnv()))
	require.NoError(t, err)
	key := base64.StdEncoding.EncodeToString(hubtest.TestTokenKey)
	for _, out := range []string{
		cfg.String(), fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg),
	} {
		assert.NotContains(t, out, "client-secret")
		assert.NotContains(t, out, key)
		assert.False(t, strings.Contains(out, string(hubtest.TestTokenKey)))
		assert.Contains(t, out, "client-id")
	}
}

func TestLoadConfigImageBaseURL(t *testing.T) {
	cfg, err := hubrise.LoadConfig(env(validEnv()))
	require.NoError(t, err)
	assert.Empty(t, cfg.ImageBaseURL, "the images cannot be published without a bucket")

	m := validEnv()
	m["AWS_URL"] = "https://aws.example/bucket/"
	cfg, _ = hubrise.LoadConfig(env(m))
	assert.Equal(t, "https://aws.example/bucket", cfg.ImageBaseURL, "AWS_URL is the last fallback")
	m["S3_BUCKET_URL"] = "https://s3.example/bucket"
	cfg, _ = hubrise.LoadConfig(env(m))
	assert.Equal(t, "https://s3.example/bucket", cfg.ImageBaseURL, "S3_BUCKET_URL comes first")
	m["S3_BUCKET_URL"] = "not a url"
	cfg, err = hubrise.LoadConfig(env(m))
	require.NoError(t, err, "a bucket setting that is not a URL is not this module's to refuse")
	assert.Equal(t, "https://aws.example/bucket", cfg.ImageBaseURL)

	m["HUBRISE_IMAGE_BASE_URL"] = " https://cdn.example/media/ "
	cfg, err = hubrise.LoadConfig(env(m))
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example/media", cfg.ImageBaseURL, "the module's own setting wins")

	m["HUBRISE_IMAGE_BASE_URL"] = "ftp://nope"
	_, err = hubrise.LoadConfig(env(m))
	require.Error(t, err, "its own setting is strict")
	assert.Contains(t, err.Error(), "HUBRISE_IMAGE_BASE_URL")
}
